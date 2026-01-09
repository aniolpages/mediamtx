package api

import (
	"time"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/logger"
)

// syncPathConfigFromRuntime syncs the path configuration from the runtime PathManager state.
// This is used after adding/removing push targets to ensure config reflects runtime state.
func (a *API) syncPathConfigFromRuntime(pathName string) {
	go func() {
		// Small delay to ensure runtime state is updated first
		time.Sleep(100 * time.Millisecond)

		a.mutex.Lock()
		defer a.mutex.Unlock()

		newConf := a.Conf.Clone()

		// Get the runtime path to check if it exists
		_, err := a.PathManager.APIPathsGet(pathName)
		if err != nil {
			a.Log(logger.Warn, "path %s not found in runtime, skipping config sync: %v", pathName, err)
			return
		}

		// Get current push targets from runtime
		pushTargets := make(conf.PushTargets, 0)
		if pushTargetsData, err := a.PathManager.APIPushTargetsList(pathName); err == nil {
			for _, pt := range pushTargetsData.Items {
				pushTargets = append(pushTargets, conf.PushTarget{URL: pt.URL})
			}
		}

		// Ensure the path exists in OptionalPaths
		if newConf.OptionalPaths == nil {
			newConf.OptionalPaths = make(map[string]*conf.OptionalPath)
		}

		// Create or update the path with push targets
		if _, ok := newConf.OptionalPaths[pathName]; ok {
			// Path exists, just update push targets
			type pushTargetsUpdate struct {
				PushTargets *conf.PushTargets `json:"pushTargets,omitempty"`
			}
			optPath := &conf.OptionalPath{
				Values: &pushTargetsUpdate{
					PushTargets: &pushTargets,
				},
			}
			if err := newConf.PatchPath(pathName, optPath); err != nil {
				a.Log(logger.Warn, "failed to patch existing path with push targets: %v", err)
				return
			}
		} else {
			// Path doesn't exist in config, create it with just push targets
			type pathWithPushTargets struct {
				PushTargets *conf.PushTargets `json:"pushTargets,omitempty"`
			}
			newConf.OptionalPaths[pathName] = &conf.OptionalPath{
				Values: &pathWithPushTargets{
					PushTargets: &pushTargets,
				},
			}
		}

		// Validate the config
		if err := newConf.Validate(nil); err != nil {
			a.Log(logger.Warn, "failed to validate config after syncing push targets: %v", err)
			return
		}

		a.Conf = newConf
		a.Parent.APIConfigSet(newConf)
		a.persistConfig()
	}()
}
