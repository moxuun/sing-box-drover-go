package config

// ReadValidatedConfig reads the configured source, decodes JSON or BPF content,
// and applies the same minimum checks used during controller startup.
func ReadValidatedConfig(path string) (ConfigSource, SingBoxConfig, error) {
	source, err := ReadConfigSource(path)
	if err != nil {
		return ConfigSource{}, SingBoxConfig{}, err
	}
	cfg, err := ReadSingBoxConfig(source.JSONText)
	if err != nil {
		return ConfigSource{}, SingBoxConfig{}, err
	}
	if err := CheckSingBoxConfig(cfg); err != nil {
		return ConfigSource{}, SingBoxConfig{}, err
	}
	return source, cfg, nil
}
