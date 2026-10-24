package config

import (
	"os"
	"path/filepath"

	"github.com/moutansos/op/internal/domain"
)

func Load(path string) (LoadResult, error) {
	return LoadWithOptions(path, ExpandOptions{})
}

func LoadWithOptions(path string, expandOptions ExpandOptions) (LoadResult, error) {
	absolute, err := absolutePath(path)
	if err != nil {
		return LoadResult{}, domain.ResourceError(domain.ErrorCodeConfig, "config.load", path, "normalize configuration path", err)
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		code := domain.ErrorCodeConfig
		if os.IsNotExist(err) {
			code = domain.ErrorCodeNotFound
		}
		return LoadResult{}, domain.ResourceError(code, "config.load", absolute, "read configuration", err)
	}
	config, warnings, err := Migrate(data)
	if err != nil {
		return LoadResult{}, err
	}
	config.SourcePath = absolute
	config.RootDirectory = filepath.Dir(absolute)
	if expandOptions.BaseDirectory == "" {
		expandOptions.BaseDirectory = config.RootDirectory
	}
	if err := Expand(&config, expandOptions); err != nil {
		return LoadResult{}, err
	}
	if err := Validate(config); err != nil {
		return LoadResult{}, err
	}
	return LoadResult{Config: config, Warnings: warnings}, nil
}

func LocateAndLoad(options LocateOptions) (LoadResult, error) {
	path, err := Locate(options)
	if err != nil {
		return LoadResult{}, err
	}
	return Load(path)
}
