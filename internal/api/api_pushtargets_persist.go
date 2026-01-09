package api

import (
	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/logger"
)

// syncPathConfigFromRuntime syncs the path configuration from the runtime PathManager state.
// This is used after adding/removing push targets to ensure config reflects runtime state.
func (a *API) syncPathConfigFromRuntime(pathName string) {
	go func() {
		a.mutex.Lock()
		defer a.mutex.Unlock()

		newConf := a.Conf.Clone()

		// Ensure the path exists in OptionalPaths
		if newConf.OptionalPaths == nil {
			newConf.OptionalPaths = make(map[string]*conf.OptionalPath)
		}

		if _, ok := newConf.OptionalPaths[pathName]; !ok {
			// Path doesn't exist - create it
			newConf.OptionalPaths[pathName] = &conf.OptionalPath{
				
			}
		}

		// Validate to populate Paths map
		if err := newConf.Validate(nil); err != nil {
			a.Log(logger.Warn, "failed to validate config when syncing path: %v", err)
			return
		}

		// Get the computed path
		pathConf, ok := newConf.Paths[pathName]
		if !ok {
			a.Log(logger.Warn, "path %s not found after validation", pathName)
			return
		}

		// Build push targets list from runtime data
		pushTargets := make(conf.PushTargets, 0)
		if pushTargetsData, err := a.PathManager.APIPushTargetsList(pathName); err == nil {
			for _, pt := range pushTargetsData.Items {
				pushTargets = append(pushTargets, conf.PushTarget{URL: pt.URL})
			}
		}

		pathConf.PushTargets = pushTargets

		// Create an OptionalPath with the updated push targets
		type pushTargetsOnly struct {
			PushTargets *conf.PushTargets `json:"pushTargets,omitempty"`
		}
		optPath := &conf.OptionalPath{
			Values: &pushTargetsOnly{
				PushTargets: &pushTargets,
			},
		}

		// Patch the path in OptionalPaths
		if err := newConf.PatchPath(pathName, optPath); err != nil {
			a.Log(logger.Warn, "failed to patch path config with push targets: %v", err)
			return
		}

		a.Conf = newConf
		a.Parent.APIConfigSet(newConf)
		a.persistConfig()
	}()
}
