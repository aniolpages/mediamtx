// Package snapshot contains the snapshot server.
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/bluenviron/mediamtx/internal/auth"
	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/externalcmd"
	"github.com/bluenviron/mediamtx/internal/hooks"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/httpp"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/unit"
	"github.com/bluenviron/mediamtx/pkg/frameextractor"
)

type serverPathManager interface {
	AddReader(req defs.PathAddReaderReq) (*defs.PathAddReaderRes, error)
}

type snapshotReader struct {
	id     uuid.UUID
	cancel func()
}

func (r *snapshotReader) Close() {
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *snapshotReader) APIReaderDescribe() *defs.APIPathReader {
	return &defs.APIPathReader{
		Type: defs.APIPathReaderTypeHidden,
		ID:   r.id.String(),
	}
}

// Server is the snapshot server.
type Server struct {
	Address         string
	DumpPackets     bool
	Encryption      bool
	ServerKey       string
	ServerCert      string
	AllowOrigins    []string
	TrustedProxies  conf.IPNetworks
	ReadTimeout     conf.Duration
	WriteTimeout    conf.Duration
	SnapshotTimeout conf.Duration
	ExternalCmdPool *externalcmd.Pool
	PathManager     serverPathManager
	Parent          logger.Writer

	httpServer *httpp.Server
}

// Initialize initializes Server.
func (s *Server) Initialize() error {
	router := gin.New()
	router.SetTrustedProxies(s.TrustedProxies.ToTrustedProxies()) //nolint:errcheck
	router.Use(s.middlewarePreflightRequests)

	router.GET("/*path", s.onRequest)

	s.httpServer = &httpp.Server{
		Address:           s.Address,
		AllowOrigins:      s.AllowOrigins,
		DumpPackets:       s.DumpPackets,
		DumpPacketsPrefix: "snapshot_server_conn",
		ReadTimeout:       time.Duration(s.ReadTimeout),
		WriteTimeout:      time.Duration(s.WriteTimeout),
		Encryption:        s.Encryption,
		ServerCert:        s.ServerCert,
		ServerKey:         s.ServerKey,
		Handler:           router,
		Parent:            s,
	}
	err := s.httpServer.Initialize()
	if err != nil {
		return err
	}

	str := "started with listener on " + s.Address
	if !s.Encryption {
		str += " (TCP/HTTP)"
	} else {
		str += " (TCP/HTTPS)"
	}
	s.Log(logger.Info, str)

	return nil
}

// Close closes Server.
func (s *Server) Close() {
	s.Log(logger.Info, "closing")
	s.httpServer.Close()
}

// Log implements logger.Writer.
func (s *Server) Log(level logger.Level, format string, args ...any) {
	s.Parent.Log(level, "[snapshot] "+format, args...)
}

func (s *Server) middlewarePreflightRequests(ctx *gin.Context) {
	if ctx.Request.Method == http.MethodOptions &&
		ctx.Request.Header.Get("Access-Control-Request-Method") != "" {
		ctx.Header("Access-Control-Allow-Methods", "OPTIONS, GET")
		ctx.Header("Access-Control-Allow-Headers", "Authorization")
		ctx.AbortWithStatus(http.StatusNoContent)
		return
	}
}

func (s *Server) writeError(ctx *gin.Context, status int, err error) {
	s.Log(logger.Error, err.Error())
	s.writeErrorNoLog(ctx, status, err)
}

func (s *Server) writeErrorNoLog(ctx *gin.Context, status int, err error) {
	ctx.AbortWithStatusJSON(status, &defs.APIError{
		Status: defs.APIErrorStatusError,
		Error:  err.Error(),
	})
}

func (s *Server) onRequest(ctx *gin.Context) {
	pathName, opts, err := parseRequest(ctx)
	if err != nil {
		s.writeErrorNoLog(ctx, http.StatusBadRequest, err)
		return
	}

	byts, contentType, err := s.snapshot(ctx, pathName, opts)
	if err != nil {
		status := http.StatusInternalServerError
		authErr, isAuthErr := errors.AsType[*auth.Error](err)
		_, isNoStreamErr := errors.AsType[*defs.PathNoStreamAvailableError](err)

		switch {
		case isAuthErr:
			if authErr.AskCredentials {
				ctx.Header("WWW-Authenticate", `Basic realm="mediamtx"`)
			}
			status = http.StatusUnauthorized

		case isNoStreamErr:
			status = http.StatusNotFound

		case errors.Is(err, errNoSupportedCodecs):
			status = http.StatusUnsupportedMediaType

		case errors.Is(err, errSnapshotTimedOut):
			status = http.StatusGatewayTimeout

		case errors.Is(err, context.Canceled):
			status = http.StatusRequestTimeout

		case errors.Is(err, frameextractor.ErrUnsupportedCodec),
			errors.Is(err, frameextractor.ErrUnsupportedFrame):
			status = http.StatusUnprocessableEntity
		}

		if status == http.StatusInternalServerError {
			s.writeError(ctx, status, err)
		} else {
			if isAuthErr && !authErr.AskCredentials {
				s.Log(logger.Info, "connection %v failed to authenticate: %v", httpp.RemoteAddr(ctx), authErr.Wrapped)
				s.writeErrorNoLog(ctx, status, fmt.Errorf("authentication error"))
			} else if isAuthErr {
				s.writeErrorNoLog(ctx, status, fmt.Errorf("authentication error"))
			} else {
				s.writeErrorNoLog(ctx, status, err)
			}
		}
		return
	}

	ctx.Header("Cache-Control", "no-store")
	ctx.Header("Content-Type", contentType)
	ctx.Writer.WriteHeader(http.StatusOK)
	ctx.Writer.Write(byts) //nolint:errcheck
}

func parseRequest(ctx *gin.Context) (string, frameextractor.Options, error) {
	rawPath := strings.TrimPrefix(ctx.Param("path"), "/")
	query := ctx.Request.URL.Query()

	var pathName string
	var format frameextractor.ImageFormat

	if rawPath == "snapshot" {
		pathName = query.Get("path")
		switch strings.ToLower(query.Get("format")) {
		case "", "jpg", "jpeg":
			format = frameextractor.FormatJPEG
		case "png":
			format = frameextractor.FormatPNG
		default:
			return "", frameextractor.Options{}, fmt.Errorf("invalid format")
		}
	} else {
		cleanPath := path.Clean(rawPath)
		cleanPath = strings.TrimLeft(cleanPath, "/\\")

		switch {
		case strings.HasSuffix(cleanPath, "/snapshot.jpg"):
			pathName = strings.TrimSuffix(cleanPath, "/snapshot.jpg")
			format = frameextractor.FormatJPEG

		case strings.HasSuffix(cleanPath, "/snapshot.jpeg"):
			pathName = strings.TrimSuffix(cleanPath, "/snapshot.jpeg")
			format = frameextractor.FormatJPEG

		case strings.HasSuffix(cleanPath, "/snapshot.png"):
			pathName = strings.TrimSuffix(cleanPath, "/snapshot.png")
			format = frameextractor.FormatPNG

		default:
			return "", frameextractor.Options{}, fmt.Errorf("path must end with /snapshot.jpg or /snapshot.png")
		}
	}

	if pathName == "" {
		return "", frameextractor.Options{}, fmt.Errorf("path is empty")
	}

	opts := frameextractor.Options{
		Format:  format,
		Quality: optionalInt(query.Get("quality")),
		Width:   optionalInt(query.Get("width")),
		Height:  optionalInt(query.Get("height")),
		Fit:     frameextractor.FitMode(query.Get("fit")),
	}

	if err := opts.Validate(); err != nil {
		return "", frameextractor.Options{}, err
	}

	return pathName, opts, nil
}

func optionalInt(v string) int {
	if v == "" {
		return 0
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		return -1
	}

	return n
}

var (
	errNoSupportedCodecs = fmt.Errorf("no supported codecs: only H.264 is currently supported")
	errSnapshotTimedOut  = fmt.Errorf("snapshot extraction timed out")
)

type snapshotResult struct {
	byts        []byte
	contentType string
	err         error
}

func (s *Server) snapshot(ctx *gin.Context, pathName string, opts frameextractor.Options) ([]byte, string, error) {
	readerCtx, readerCancel := context.WithTimeout(ctx.Request.Context(), time.Duration(s.SnapshotTimeout))
	defer readerCancel()

	id := uuid.New()
	reqReader := &snapshotReader{
		id:     id,
		cancel: readerCancel,
	}

	res, err := s.PathManager.AddReader(defs.PathAddReaderReq{
		Author: reqReader,
		AccessRequest: defs.PathAccessRequest{
			Name:        pathName,
			Query:       ctx.Request.URL.RawQuery,
			Publish:     false,
			UserAgent:   ctx.Request.UserAgent(),
			Proto:       auth.ProtocolSnapshot,
			ID:          &id,
			Credentials: httpp.Credentials(ctx.Request),
			IP:          net.ParseIP(ctx.ClientIP()),
		},
	})
	if err != nil {
		return nil, "", err
	}

	defer res.Path.RemoveReader(defs.PathRemoveReaderReq{Author: reqReader})

	h264Media, h264Format := findH264Format(res.Stream.OrigDesc.Medias)
	if h264Format == nil {
		return nil, "", errNoSupportedCodecs
	}

	var decoder frameextractor.H264Decoder
	err = decoder.Init()
	if err != nil {
		return nil, "", err
	}

	if h264Format.SPS != nil && h264Format.PPS != nil {
		_, err = decoder.Decode(frameextractor.AccessUnit{h264Format.SPS, h264Format.PPS})
		if err != nil && !errors.Is(err, frameextractor.ErrNoKeyFrame) {
			return nil, "", err
		}
	}

	results := make(chan snapshotResult, 1)

	streamReader := &stream.Reader{
		SkipOutboundBytes: true,
		Parent:            s,
	}
	streamReader.OnData(h264Media, h264Format, func(u *unit.Unit) error {
		payload, ok := u.Payload.(unit.PayloadH264)
		if !ok {
			return nil
		}

		byts, contentType, err := decoder.Snapshot(frameextractor.AccessUnit(payload), opts)
		if err != nil {
			if errors.Is(err, frameextractor.ErrNoKeyFrame) {
				return nil
			}
		}

		select {
		case results <- snapshotResult{
			byts:        byts,
			contentType: contentType,
			err:         err,
		}:
		default:
		}

		return nil
	})

	res.Stream.AddReader(streamReader)
	defer res.Stream.RemoveReader(streamReader)

	onUnreadHook := hooks.OnRead(hooks.OnReadParams{
		Logger:          s,
		ExternalCmdPool: s.ExternalCmdPool,
		Conf:            res.Path.SafeConf(),
		ExternalCmdEnv:  res.Path.ExternalCmdEnv(),
		Reader:          *reqReader.APIReaderDescribe(),
		Query:           ctx.Request.URL.RawQuery,
	})
	defer onUnreadHook()

	select {
	case result := <-results:
		return result.byts, result.contentType, result.err

	case err = <-streamReader.Error():
		return nil, "", err

	case <-readerCtx.Done():
		if errors.Is(readerCtx.Err(), context.DeadlineExceeded) {
			return nil, "", errSnapshotTimedOut
		}
		return nil, "", readerCtx.Err()
	}
}

func findH264Format(medias []*description.Media) (*description.Media, *format.H264) {
	for _, medi := range medias {
		for _, forma := range medi.Formats {
			if forma, ok := forma.(*format.H264); ok {
				return medi, forma
			}
		}
	}

	return nil, nil
}
