package ui

import (
	_ "embed"
)

//go:embed fonts/DejaVuSans.ttf
var fontRegularTTF []byte

//go:embed fonts/DejaVuSans-Bold.ttf
var fontBoldTTF []byte
