// Code generated from Pkl module `cloudlab.Config`. DO NOT EDIT.
package config

import (
	"context"

	"github.com/apple/pkl-go/pkl"
)

type Config struct {
	BasePath *string `pkl:"basePath"`

	Region *string `pkl:"region"`

	Size *string `pkl:"size"`

	Template *string `pkl:"template"`

	Arch string `pkl:"arch"`

	Image string `pkl:"image"`

	SshKeys *[]string `pkl:"sshKeys"`

	Packages []string `pkl:"packages"`

	Flakes []Flake `pkl:"flakes"`
}

// LoadFromPath loads the pkl module at the given path and evaluates it into a Config
func LoadFromPath(ctx context.Context, path string) (ret Config, err error) {
	evaluator, err := pkl.NewEvaluator(ctx, pkl.PreconfiguredOptions)
	if err != nil {
		return ret, err
	}
	defer func() {
		cerr := evaluator.Close()
		if err == nil {
			err = cerr
		}
	}()
	ret, err = Load(ctx, evaluator, pkl.FileSource(path))
	return ret, err
}

// Load loads the pkl module at the given source and evaluates it with the given evaluator into a Config
func Load(ctx context.Context, evaluator pkl.Evaluator, source *pkl.ModuleSource) (Config, error) {
	var ret Config
	err := evaluator.EvaluateModule(ctx, source, &ret)
	return ret, err
}
