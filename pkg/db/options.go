/*
Copyright 2019 vChain, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package db

import (
	"path/filepath"

	"github.com/dgraph-io/badger/v2"
)

const (
	dataPath = "data"
	treePath = "tree"
)

type Options struct {
	Badger badger.Options
}

func DefaultOptions(path string) Options {
	opt := badger.DefaultOptions(path).
		WithSyncWrites(true).
		WithEventLogging(false)

	opt.WithMaxTableSize(opt.MaxTableSize * 2)

	return Options{
		Badger: opt,
	}
}

func (o Options) dataStore() badger.Options {
	opt := o.Badger
	basedir := opt.Dir
	opt.Dir = filepath.Join(basedir, dataPath)
	opt.ValueDir = filepath.Join(basedir, dataPath)
	return opt
}

func (o Options) treeStore() badger.Options {
	opt := o.Badger
	basedir := opt.Dir
	opt.Dir = filepath.Join(basedir, treePath)
	opt.ValueDir = filepath.Join(basedir, treePath)
	return opt
}