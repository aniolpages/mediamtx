package conf

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v2"
)

// SaveToFile saves the configuration to a YAML file.
func (conf *Conf) SaveToFile(fpath string) error {
	if fpath == "" {
		return fmt.Errorf("config file path is empty")
	}

	// Marshal the config to YAML
	byts, err := yaml.Marshal(conf)
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
