// Code generated from Pkl module `bivouac.Config`. DO NOT EDIT.
package config

type HerdrPane struct {
	Label string `pkl:"label"`

	// Run once, when the pane is first created.
	Command *string `pkl:"command"`

	// An absolute path, or one starting `~`, is passed to herdr
	// unchanged -- both are resolved on the instance itself. Anything
	// else is relative to the session's checkout on the instance;
	// omitted means the checkout itself.
	Cwd *string `pkl:"cwd"`
}
