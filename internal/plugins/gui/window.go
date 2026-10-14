package gui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// content keeps the demo body empty. Only the profile header changes when
// the owner picks another profile; no node is started and nothing is saved.
func content(page page) fyne.CanvasObject {
	active := widget.NewLabel("")
	id := widget.NewLabel("")
	names := make([]string, len(page.choices))
	for i, p := range page.choices {
		// Numbering also keeps unnamed profiles unambiguous to Select.
		names[i] = fmt.Sprintf("%d · %s", i+1, p.name)
	}
	picker := widget.NewSelect(names, nil)
	picker.OnChanged = func(_ string) {
		i := picker.SelectedIndex()
		if i < 0 {
			return
		}
		p := page.choices[i]
		active.SetText("Active profile: " + p.name)
		if p.id == "" {
			id.SetText("Profile not set up yet")
		} else {
			id.SetText(p.id)
		}
	}
	picker.SetSelectedIndex(page.selected)
	if len(names) == 1 {
		picker.Disable()
	}
	header := container.NewVBox(
		widget.NewLabelWithStyle("Ouroboros", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewBorder(nil, nil, widget.NewLabel("Profile"), nil, picker),
		active, id, widget.NewSeparator(),
	)
	return container.NewBorder(header, nil, nil, nil, container.NewWithoutLayout())
}
