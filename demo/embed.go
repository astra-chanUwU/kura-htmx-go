package demomedia

import "embed"

// Files contains the reproducible local demo originals and thumbnails.
//
//go:embed images/* thumbs/*
var Files embed.FS
