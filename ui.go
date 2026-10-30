// This file is part of the program "NoiseTorch-ng".
// Please see the LICENSE file for copyright information.

package main

import (
	"fmt"
	"log"
	"os"
	"syscall"
	"time"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/noisetorch/pulseaudio"
)

type ntcontext struct {
	inputList           []device
	outputList          []device
	noiseSupressorState int
	paClient            *pulseaudio.Client
	librnnoise          string
	pipeWirePluginReady bool
	config              *config
	app                 *gtk.Application
	window              *gtk.ApplicationWindow
	update              updateui
	reloadRequired      bool
	haveCapabilities    bool
	capsMismatch        bool
	views               *ViewStack
	serverInfo          audioserverinfo
	virtualDeviceInUse  bool
}

// TODO pull some of these strucs out of UI, they don't belong here
type audioserverinfo struct {
	servertype       uint
	name             string
	major            int
	minor            int
	patch            int
	outdatedPipeWire bool
}

const (
	servertype_pulse = iota
	servertype_pipewire
)

const (
	colorGreen     = "#22bb45"
	colorRed       = "#ff4646"
	colorOrange    = "#ff8c00"
	colorLightBlue = "#add8e6"
)

const notice = "NoiseTorch Next Gen (stylized NoiseTorch-ng) is a continuation of the NoiseTorch\nproject after it was abandoned by its original author. Please do not confuse\nboth programs. You may convey modified versions of this program under its name."

// refreshUI schedules a rebuild of the current view on the GTK main thread.
// It is safe to call from any goroutine.
func refreshUI(ctx *ntcontext) {
	glib.IdleAdd(func() bool {
		rebuildUI(ctx)
		return false
	})
}

func rebuildUI(ctx *ntcontext) {
	if ctx.window == nil {
		return
	}
	currView := ctx.views.Peek()
	if currView == nil {
		// the stack can be emptied when the audio server restarts mid-load
		// and the connection watchdog resets the UI
		currView = mainView
		ctx.views.Push(currView)
	}
	scrolled := gtk.NewScrolledWindow()
	scrolled.SetChild(currView(ctx))
	ctx.window.SetChild(scrolled)
}

func uiPush(ctx *ntcontext, v ViewFunc) {
	glib.IdleAdd(func() bool {
		ctx.views.Push(v)
		rebuildUI(ctx)
		return false
	})
}

func uiPop(ctx *ntcontext) {
	glib.IdleAdd(func() bool {
		ctx.views.Pop()
		rebuildUI(ctx)
		return false
	})
}

func newViewBox() *gtk.Box {
	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.SetMarginStart(12)
	box.SetMarginEnd(12)
	box.SetMarginTop(12)
	box.SetMarginBottom(12)
	return box
}

func centeredLabel(text string) *gtk.Label {
	l := gtk.NewLabel(text)
	l.SetHAlign(gtk.AlignCenter)
	l.SetJustify(gtk.JustifyCenter)
	l.SetWrap(true)
	return l
}

func coloredLabel(text, color string) *gtk.Label {
	l := gtk.NewLabel("")
	l.SetMarkup("<span foreground='" + color + "'>" + glib.MarkupEscapeText(text) + "</span>")
	l.SetHAlign(gtk.AlignCenter)
	l.SetJustify(gtk.JustifyCenter)
	l.SetWrap(true)
	return l
}

func mainView(ctx *ntcontext) gtk.Widgetter {
	box := newViewBox()

	if ctx.noiseSupressorState == loaded {
		if ctx.virtualDeviceInUse {
			box.Append(coloredLabel("Filtering active", colorGreen))
		} else {
			box.Append(coloredLabel("Filtering unconfigured", colorLightBlue))
		}
	} else if ctx.noiseSupressorState == unloaded {
		_, inpOk := inputSelection(ctx)
		_, outOk := outputSelection(ctx)
		if validConfiguration(ctx, inpOk, outOk) {
			box.Append(coloredLabel("Filtering inactive", colorRed))
		} else {
			box.Append(coloredLabel("Filtering unconfigured", colorLightBlue))
		}
	} else if ctx.noiseSupressorState == inconsistent {
		box.Append(coloredLabel("Inconsistent state, please unload first.", colorOrange))
	}

	if ctx.serverInfo.servertype == servertype_pipewire {
		l := gtk.NewLabel("Running in PipeWire mode. PipeWire support is currently alpha quality. Please report bugs.")
		l.SetWrap(true)
		box.Append(l)
	}

	if ctx.update.available && !ctx.update.triggered {
		row := gtk.NewBox(gtk.OrientationHorizontal, 8)
		l := gtk.NewLabel("Update available! Click to install version: " + ctx.update.serverVersion)
		l.SetMarkup("<span foreground='" + colorGreen + "'>" + glib.MarkupEscapeText(l.Text()) + "</span>")
		l.SetHExpand(true)
		row.Append(l)
		btn := gtk.NewButtonWithLabel("Update")
		btn.ConnectClicked(func() {
			ctx.update.triggered = true
			go update(ctx)
			refreshUI(ctx)
		})
		row.Append(btn)
		box.Append(row)
	}

	if ctx.update.triggered {
		box.Append(centeredLabel(ctx.update.updatingText))
	}

	settings := gtk.NewBox(gtk.OrientationVertical, 6)
	settingsExp := gtk.NewExpander("Settings")
	settingsExp.SetExpanded(true)
	settingsExp.SetChild(settings)
	box.Append(settingsExp)

	monCb := gtk.NewCheckButtonWithLabel("Display Monitor Sources")
	monCb.SetActive(ctx.config.DisplayMonitorSources)
	monCb.ConnectToggled(func() {
		ctx.config.DisplayMonitorSources = monCb.Active()
		go writeConfig(ctx.config)
		refreshUI(ctx)
	})
	settings.Append(monCb)

	thresholdRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	thresholdLabel := gtk.NewLabel("Voice Activation Threshold")
	thresholdLabel.SetTooltipText("If you have a decent microphone, you can usually turn this all the way up.")
	thresholdRow.Append(thresholdLabel)
	thresholdScale := gtk.NewScaleWithRange(gtk.OrientationHorizontal, 0, 95, 1)
	thresholdScale.SetValue(float64(ctx.config.Threshold))
	thresholdScale.SetDrawValue(false)
	thresholdScale.SetHExpand(true)
	thresholdScale.SetTooltipText("If you have a decent microphone, you can usually turn this all the way up.")
	thresholdRow.Append(thresholdScale)
	thresholdPct := gtk.NewLabel(fmt.Sprintf("%d%%", ctx.config.Threshold))
	thresholdRow.Append(thresholdPct)
	settings.Append(thresholdRow)

	reloadLabel := coloredLabel("Reloading the filter(s) is required to apply these changes.", colorOrange)
	reloadLabel.SetHAlign(gtk.AlignStart)
	reloadLabel.SetJustify(gtk.JustifyLeft)
	if ctx.reloadRequired {
		settings.Append(reloadLabel)
	}

	thresholdScale.ConnectValueChanged(func() {
		ctx.config.Threshold = int(thresholdScale.Value())
		thresholdPct.SetText(fmt.Sprintf("%d%%", ctx.config.Threshold))
		go writeConfig(ctx.config)
		ctx.reloadRequired = true
		if !reloadLabel.Visible() {
			reloadLabel.Show()
		}
	})

	filterInputCb := gtk.NewCheckButtonWithLabel("Filter Microphone")
	filterInputCb.SetActive(ctx.config.FilterInput)
	filterInputCb.ConnectToggled(func() {
		ctx.config.FilterInput = filterInputCb.Active()
		go writeConfig(ctx.config)
		go func() { ctx.noiseSupressorState, _ = supressorState(ctx); refreshUI(ctx) }()
		refreshUI(ctx)
	})
	settings.Append(filterInputCb)

	filterOutputCb := gtk.NewCheckButtonWithLabel("Filter Headphones")
	filterOutputCb.SetActive(ctx.config.FilterOutput)
	filterOutputCb.ConnectToggled(func() {
		ctx.config.FilterOutput = filterOutputCb.Active()
		go writeConfig(ctx.config)
		go func() { ctx.noiseSupressorState, _ = supressorState(ctx); refreshUI(ctx) }()
		refreshUI(ctx)
	})
	settings.Append(filterOutputCb)

	if ctx.config.FilterInput {
		box.Append(deviceSelector(ctx, "Select Microphone", "Select an input device below:", &ctx.inputList))
	}

	if ctx.config.FilterOutput {
		box.Append(deviceSelector(ctx, "Select Headphones", "Select an output device below:", &ctx.outputList))
	}

	buttonRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	buttonRow.SetHAlign(gtk.AlignCenter)
	box.Append(buttonRow)

	if ctx.noiseSupressorState != unloaded {
		unloadBtn := gtk.NewButtonWithLabel("Unload Filter(s)")
		unloadBtn.ConnectClicked(func() {
			ctx.reloadRequired = false
			if ctx.virtualDeviceInUse {
				confirm := makeConfirmView(ctx,
					"Virtual Device in Use",
					"Some applications may behave weirdly when you remove a device they're currently using",
					"Unload",
					"Go back",
					func() { uiUnloadFilters(ctx) },
					func() {})
				uiPush(ctx, confirm)
			} else {
				go uiUnloadFilters(ctx)
			}
		})
		buttonRow.Append(unloadBtn)
	}

	txt := "Load Filter(s)"
	if ctx.noiseSupressorState == loaded {
		txt = "Reload Filter(s)"
	}

	inp, inpOk := inputSelection(ctx)
	out, outOk := outputSelection(ctx)
	if validConfiguration(ctx, inpOk, outOk) {
		loadBtn := gtk.NewButtonWithLabel(txt)
		loadBtn.ConnectClicked(func() {
			ctx.reloadRequired = false
			if ctx.virtualDeviceInUse {
				confirm := makeConfirmView(ctx,
					"Virtual Device in Use",
					"Some applications may behave weirdly when you reload a device they're currently using",
					"Reload",
					"Go back",
					func() { uiReloadFilters(ctx, inp, out) },
					func() {})
				uiPush(ctx, confirm)
			} else {
				go uiReloadFilters(ctx, inp, out)
			}
		})
		buttonRow.Append(loadBtn)
	}

	return box
}

func deviceSelector(ctx *ntcontext, title, hint string, devices *[]device) gtk.Widgetter {
	inner := gtk.NewBox(gtk.OrientationVertical, 4)
	inner.Append(gtk.NewLabel(hint))

	for i := range *devices {
		el := &(*devices)[i]

		if el.isMonitor && !ctx.config.DisplayMonitorSources {
			continue
		}

		cb := gtk.NewCheckButton()
		cb.SetActive(el.checked)
		row := gtk.NewBox(gtk.OrientationHorizontal, 6)
		row.Append(cb)
		if el.dynamicLatency {
			row.Append(gtk.NewLabel(el.Name))
		} else {
			name := gtk.NewLabel("(incompatible?) " + el.Name)
			name.SetMarkup("<span foreground='" + colorOrange + "'>" + glib.MarkupEscapeText(name.Text()) + "</span>")
			row.Append(name)
		}
		cb.ConnectToggled(func() {
			el.checked = cb.Active()
			ensureOnlyOneInputSelected(devices, el)
			refreshUI(ctx)
		})
		inner.Append(row)
	}

	exp := gtk.NewExpander(title)
	exp.SetExpanded(true)
	exp.SetChild(inner)
	return exp
}

func uiUnloadFilters(ctx *ntcontext) {
	uiPush(ctx, loadingView)
	if err := unloadSupressor(ctx); err != nil {
		log.Println(err)
		resetUI(ctx)
		uiPush(ctx, makeErrorView(ctx, fmt.Sprintf("Could not unload filter(s): %v", err)))
		return
	}
	//wait until PA reports it has actually loaded it, timeout at 10s
	for i := 0; i < 20; i++ {
		if state, _ := supressorState(ctx); state != unloaded {
			time.Sleep(time.Millisecond * 500)
		}
	}
	uiPop(ctx)
}

func uiReloadFilters(ctx *ntcontext, inp, out device) {
	// make sure the LADSPA plugin is reachable before showing the loading
	// view; on PipeWire >= 1.6.3 this may restart pipewire-pulse once, which
	// makes the connection watchdog reset the view stack
	if err := ensurePipeWirePlugin(ctx); err != nil {
		log.Println(err)
		resetUI(ctx)
		uiPush(ctx, makeErrorView(ctx, err.Error()))
		return
	}

	uiPush(ctx, loadingView)
	if ctx.noiseSupressorState == loaded {
		if err := unloadSupressor(ctx); err != nil {
			log.Println(err)
		}
	}
	if err := loadSupressor(ctx, &inp, &out); err != nil {
		log.Println(err)
		resetUI(ctx)
		uiPush(ctx, makeErrorView(ctx, fmt.Sprintf("Could not load filter(s): %v", err)))
		return
	}

	//wait until PA reports it has actually loaded it, timeout at 10s
	for i := 0; i < 20; i++ {
		if state, _ := supressorState(ctx); state != loaded {
			time.Sleep(time.Millisecond * 500)
		}
	}
	ctx.config.LastUsedInput = inp.ID
	ctx.config.LastUsedOutput = out.ID
	go writeConfig(ctx.config)
	uiPop(ctx)
}

func ensureOnlyOneInputSelected(inps *[]device, current *device) {
	if !current.checked {
		return
	}
	for i := range *inps {
		el := &(*inps)[i]
		el.checked = false
	}
	current.checked = true
}

func inputSelection(ctx *ntcontext) (device, bool) {
	if !ctx.config.FilterInput {
		return device{}, false
	}

	for _, in := range ctx.inputList {
		if in.checked && (!in.isMonitor || ctx.config.DisplayMonitorSources) {
			return in, true
		}
	}
	return device{}, false
}

func outputSelection(ctx *ntcontext) (device, bool) {
	if !ctx.config.FilterOutput {
		return device{}, false
	}

	for _, out := range ctx.outputList {
		if out.checked {
			return out, true
		}
	}
	return device{}, false
}

func validConfiguration(ctx *ntcontext, inpOk bool, outOk bool) bool {
	return (!ctx.config.FilterInput || (ctx.config.FilterInput && inpOk)) &&
		(!ctx.config.FilterOutput || (ctx.config.FilterOutput && outOk)) &&
		(ctx.config.FilterInput || ctx.config.FilterOutput) &&
		ctx.noiseSupressorState != inconsistent
}

func loadingView(ctx *ntcontext) gtk.Widgetter {
	box := newViewBox()
	box.Append(centeredLabel("Working..."))
	box.Append(centeredLabel("(this may take a few seconds)"))
	return box
}

func licenseView(ctx *ntcontext) gtk.Widgetter {
	box := newViewBox()
	box.Append(centeredLabel(notice))

	tv := gtk.NewTextView()
	tv.SetEditable(false)
	tv.SetWrapMode(gtk.WrapWord)
	tv.SetMonospace(true)
	tv.Buffer().SetText(licenseString)
	box.Append(tv)

	okBtn := gtk.NewButtonWithLabel("OK")
	okBtn.SetHAlign(gtk.AlignCenter)
	okBtn.ConnectClicked(func() { uiPop(ctx) })
	box.Append(okBtn)
	return box
}

func versionView(ctx *ntcontext) gtk.Widgetter {
	box := newViewBox()
	box.Append(centeredLabel("Version"))
	box.Append(centeredLabel(notice))
	box.Append(centeredLabel(fmt.Sprintf("%s (%s)", version, distribution)))
	okBtn := gtk.NewButtonWithLabel("OK")
	okBtn.SetHAlign(gtk.AlignCenter)
	okBtn.ConnectClicked(func() { uiPop(ctx) })
	box.Append(okBtn)
	return box
}

func connectView(ctx *ntcontext) gtk.Widgetter {
	box := newViewBox()
	box.Append(centeredLabel("Connecting to pulseaudio..."))
	return box
}

func capabilitiesView(ctx *ntcontext) gtk.Widgetter {
	box := newViewBox()
	box.Append(centeredLabel("This program does not have the capabilities to function properly."))
	box.Append(centeredLabel("We require CAP_SYS_RESOURCE. If that doesn't mean anything to you, don't worry. I'll fix it for you."))
	if ctx.capsMismatch {
		box.Append(coloredLabel("Warning: File has CAP_SYS_RESOURCE but our process doesn't.", colorOrange))
		box.Append(coloredLabel("Check if your filesystem has nosuid set or check the troubleshooting page.", colorOrange))
	}
	btn := gtk.NewButtonWithLabel("Grant capability (requires root)")
	btn.SetHAlign(gtk.AlignCenter)
	btn.ConnectClicked(func() {
		err := pkexecSetcapSelf()
		if err != nil {
			uiPush(ctx, makeErrorView(ctx, err.Error()))
			return
		}
		self, err := os.Executable()
		if err != nil {
			uiPush(ctx, makeErrorView(ctx, err.Error()))
			return
		}
		err = syscall.Exec(self, []string{""}, os.Environ())
		if err != nil {
			uiPush(ctx, makeErrorView(ctx, err.Error()))
			return
		}
	})
	box.Append(btn)
	return box
}

func makeErrorView(ctx *ntcontext, errorMsg string) ViewFunc {
	return func(ctx *ntcontext) gtk.Widgetter {
		box := newViewBox()
		box.Append(centeredLabel("Error"))
		l := centeredLabel(errorMsg)
		box.Append(l)
		okBtn := gtk.NewButtonWithLabel("OK")
		okBtn.SetHAlign(gtk.AlignCenter)
		okBtn.ConnectClicked(func() { uiPop(ctx) })
		box.Append(okBtn)
		return box
	}
}

func makeFatalErrorView(ctx *ntcontext, errorMsg string) ViewFunc {
	return func(ctx *ntcontext) gtk.Widgetter {
		box := newViewBox()
		box.Append(centeredLabel("Fatal Error"))
		box.Append(centeredLabel(errorMsg))
		quitBtn := gtk.NewButtonWithLabel("Quit")
		quitBtn.SetHAlign(gtk.AlignCenter)
		quitBtn.ConnectClicked(func() { os.Exit(1) })
		box.Append(quitBtn)
		return box
	}
}

func makeConfirmView(ctx *ntcontext, title, text, confirmText, denyText string, confirmfunc, denyfunc func()) ViewFunc {
	return func(ctx *ntcontext) gtk.Widgetter {
		box := newViewBox()
		box.Append(centeredLabel(title))
		box.Append(centeredLabel(text))
		row := gtk.NewBox(gtk.OrientationHorizontal, 8)
		row.SetHAlign(gtk.AlignCenter)
		denyBtn := gtk.NewButtonWithLabel(denyText)
		denyBtn.ConnectClicked(func() {
			uiPop(ctx)
			go denyfunc()
		})
		confirmBtn := gtk.NewButtonWithLabel(confirmText)
		confirmBtn.ConnectClicked(func() {
			uiPop(ctx)
			go confirmfunc()
		})
		row.Append(denyBtn)
		row.Append(confirmBtn)
		box.Append(row)
		return box
	}
}

func resetUI(ctx *ntcontext) {
	ctx.views = NewViewStack()
	ctx.views.Push(mainView)

	if !ctx.haveCapabilities {
		ctx.views.Push(capabilitiesView)
	}

	if ctx.serverInfo.outdatedPipeWire {
		ctx.views.Push(makeFatalErrorView(ctx,
			fmt.Sprintf("Your PipeWire version is too old. Detected %d.%d.%d. Require at least 0.3.28.",
				ctx.serverInfo.major, ctx.serverInfo.minor, ctx.serverInfo.patch)))
	}
}
