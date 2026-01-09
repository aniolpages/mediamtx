package conf

import (
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v2"
)

// SaveToFile saves the configuration to a YAML file.
func (conf *Conf) SaveToFile(fpath string) error {
	if fpath == "" {
		return fmt.Errorf("config file path is empty")
	}

	// First marshal to JSON to use the custom JSON marshalers
	jsonBytes, err := json.Marshal(conf)
	if err != nil {
		return fmt.Errorf("failed to marshal config to JSON: %w", err)
	}

	// Then convert JSON to a generic map
	var genericMap map[string]interface{}
	err = json.Unmarshal(jsonBytes, &genericMap)
	if err != nil {
		return fmt.Errorf("failed to unmarshal JSON to map: %w", err)
	}

	// Now marshal the map to YAML
	byts, err := yaml.Marshal(genericMap)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write to file with proper permissions
	err = os.WriteFile(fpath, byts, 0644)
	if err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}
