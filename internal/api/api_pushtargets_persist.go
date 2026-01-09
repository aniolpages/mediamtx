package api

import (
	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/logger"
)

// syncPathConfigFromRuntime syncs the path configuration from the runtime PathManager state.
// This is used after adding/removing push targets to ensure config reflects runtime state.
func (a *API) syncPathConfigFromRuntime(pathName string) {
	// Only sync if persistence is enabled
	if !a.Conf.APIPersistChanges {
		return
	}

	go func() {
		// Recover from any panics to prevent server crash
		defer func() {
			if r := recover(); r != nil {
				a.Log(logger.Error, "panic in syncPathConfigFromRuntime for path %s: %v", pathName, r)
			}
		}()

		a.mutex.Lock()
		defer a.mutex.Unlock()

		// Build push targets list from runtime data
		pushTargetsData, err := a.PathManager.APIPushTargetsList(pathName)
		if err != nil {
			a.Log(logger.Debug, "cannot sync push targets for path %s: %v", pathName, err)
			return
		}

		pushTargets := make(conf.PushTargets, 0)
		for _, pt := range pushTargetsData.Items {
			pushTargets = append(pushTargets, conf.PushTarget{URL: pt.URL})
		}

		newConf := a.Conf.Clone()

		// Ensure the path exists in OptionalPaths
		if newConf.OptionalPaths == nil {
			newConf.OptionalPaths = make(map[string]*conf.OptionalPath)
		}

		// Create an OptionalPath with the updated push targets
		type pushTargetsOnly struct {
			PushTargets *conf.PushTargets `json:"pushTargets,omitempty"`
		}
		optPath := &conf.OptionalPath{
			Values: &pushTargetsOnly{
				PushTargets: &pushTargets,
			},
		}

		// Check if path already exists in config
		if _, exists := newConf.OptionalPaths[pathName]; exists {
			// Path exists - patch it
			if err := newConf.PatchPath(pathName, optPath); err != nil {
				a.Log(logger.Warn, "failed to patch path config with push targets: %v", err)
				return
			}
		} else {
			// Path doesn't exist - create it directly
			newConf.OptionalPaths[pathName] = optPath
		}

		a.Conf = newConf
		a.Parent.APIConfigSet(newConf)
		a.persistConfig()
	}()
}
