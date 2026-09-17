//go:build !linux || !cgo

package handlers

func augmentWithCgroupStatus(map[string]any) {}

func shouldAugmentCgroupStatus() bool { return false }
