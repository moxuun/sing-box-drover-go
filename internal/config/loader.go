package config

import (
	"errors"
	"fmt"
	"os"
)

// ReadValidatedConfig reads the configured JSON source and applies the same
// minimum checks used during controller startup. Only the path and the derived
// runtime JSON outlive the call; the source text is not kept.
func ReadValidatedConfig(path string) (ConfigSource, SingBoxConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ConfigSource{}, SingBoxConfig{}, fmt.Errorf("configuration file not found")
		}
		return ConfigSource{}, SingBoxConfig{}, fmt.Errorf("failed to read configuration file: %w", err)
	}
	cfg, err := ReadSingBoxConfig(readUTF8(data))
	if err != nil {
		return ConfigSource{}, SingBoxConfig{}, err
	}
	if err := CheckSingBoxConfig(cfg); err != nil {
		return ConfigSource{}, SingBoxConfig{}, err
	}
	return ConfigSource{FilePath: path}, cfg, nil
}
