//go:build !linux

package graph

import "context"

// No file watcher on this platform: refresh scans the tree instead.
type watcher struct{} //lint:ignore U1000 Service.w's type on platforms without a watcher

func (s *Service) watchInit() bool           { return false }
func (s *Service) watchLoop(context.Context) {}
func (s *Service) drain()                    {}
