//go:build !linux

package graph

import "context"

// No file watcher on this platform: refresh scans the tree instead.
type watcher struct{}

func (s *Service) watchInit() bool           { return false }
func (s *Service) watchLoop(context.Context) {}
func (s *Service) drain()                    {}
