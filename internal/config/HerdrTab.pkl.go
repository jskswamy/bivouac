// Code generated from Pkl module `bivouac.Config`. DO NOT EDIT.
package config

type HerdrTab struct {
	Label string `pkl:"label"`

	// In declaration order: the first is the tab's own pane, and each
	// later one splits off the one before it.
	Panes []HerdrPane `pkl:"panes"`
}
