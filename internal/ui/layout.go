package ui

import "kindleiot/internal/config"

type placedWidget struct {
	w       config.WidgetConfig
	col     int
	row     int
	span    int
	rowSpan int
}

// layoutGrid places widgets onto a 2D grid of `cols` columns in two passes:
// Pass 1 reserves widgets that declare explicit (GridCol, GridRow) positions when free;
// Pass 2 flows remaining or colliding widgets into the earliest available 2D slot,
// avoiding sub-page splits for multi-row blocks when rowsPerPage > 0.
func layoutGrid(widgets []config.WidgetConfig, cols, rowsPerPage int) ([]placedWidget, int) {
	if cols < 1 {
		cols = 1
	}
	rowLimit := 6
	if rowsPerPage > 0 {
		rowLimit = rowsPerPage
	}

	occ := make(map[[2]int]bool)
	canPlace := func(c, r, sw, sh int) bool {
		if c < 0 || r < 0 || c+sw > cols {
			return false
		}
		for dy := 0; dy < sh; dy++ {
			for dx := 0; dx < sw; dx++ {
				if occ[[2]int{c + dx, r + dy}] {
					return false
				}
			}
		}
		return true
	}
	mark := func(c, r, sw, sh int) {
		for dy := 0; dy < sh; dy++ {
			for dx := 0; dx < sw; dx++ {
				occ[[2]int{c + dx, r + dy}] = true
			}
		}
	}

	placed := make([]placedWidget, len(widgets))
	needsAuto := make([]bool, len(widgets))

	// Pass 1: explicitly positioned cards.
	for i, wd := range widgets {
		sw := 1
		if wd.Span > 0 {
			sw = clampInt(wd.Span, 1, cols)
		}
		sh := wd.EffectiveRowSpan(rowLimit)
		if wd.GridCol != nil && wd.GridRow != nil {
			c := clampInt(*wd.GridCol, 0, cols-sw)
			r := *wd.GridRow
			if r < 0 {
				r = 0
			}
			if canPlace(c, r, sw, sh) {
				mark(c, r, sw, sh)
				placed[i] = placedWidget{w: wd, col: c, row: r, span: sw, rowSpan: sh}
				continue
			}
		}
		placed[i] = placedWidget{w: wd, span: sw, rowSpan: sh}
		needsAuto[i] = true
	}

	// Pass 2: sequential scan from top-left to bottom-right for unpinned or displaced cards.
	for i := range widgets {
		if !needsAuto[i] {
			continue
		}
		sw := placed[i].span
		sh := placed[i].rowSpan
		found := false
		for r := 0; !found; r++ {
			if rowsPerPage > 0 && sh <= rowsPerPage && (r/rowsPerPage) != ((r+sh-1)/rowsPerPage) {
				r = ((r/rowsPerPage)+1)*rowsPerPage - 1
				continue
			}
			for c := 0; c <= cols-sw; c++ {
				if canPlace(c, r, sw, sh) {
					mark(c, r, sw, sh)
					placed[i].col = c
					placed[i].row = r
					found = true
					break
				}
			}
		}
	}

	totalRows := 0
	for _, item := range placed {
		if end := item.row + item.rowSpan; end > totalRows {
			totalRows = end
		}
	}
	return placed, totalRows
}
