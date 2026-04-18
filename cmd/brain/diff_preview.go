package main

import (
	"encoding/json"

	"github.com/leef-l/brain/cmd/brain/diff"
)

type diffOp = diff.Op
type fileSnapshot = diff.FileSnapshot

var (
	buildPreExecPreview = diff.BuildPreExecPreview
	buildPostExecDiff   = diff.BuildPostExecDiff
	colorizeDiffLines   = diff.ColorizeDiffLines
)

func snapshotForTool(workdir, toolName string, args json.RawMessage) *fileSnapshot {
	return diff.SnapshotForTool(workdir, toolName, args)
}
