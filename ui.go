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

const notice = "NoiseTorch Next Gen (stylized NoiseTorch-ng) is a continuation of the NoiseTorch\nproject after it was abandoned by its original author. Please do not confuse\nboth programs. You may convey modified versions of this program under its name."

// uiDo runs f on the GTK main thread. It is safe to call from any goroutine.
func uiDo(f func()) {
	glib.IdleAdd(func() bool {
		f()
		return false
	})
}

// refreshUI schedules a rebuild of the current view on the GTK main thread.
func refreshUI(ctx *ntcontext) {
	uiDo(func() { rebuildUI(ctx) })
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
	scrolled.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	scrolled.SetHExpand(true)
	scrolled.SetVExpand(true)
	scrolled.SetChild(currView(ctx))
	ctx.window.SetChild(scrolled)
}

func uiPush(ctx *ntcontext, v ViewFunc) {
	uiDo(func() {
		ctx.views.Push(v)
		rebuildUI(ctx)
	})
}

func uiPop(ctx *ntcontext) {
	uiDo(func() {
		ctx.views.Pop()
		rebuildUI(ctx)
	})
}

// uiReset rebuilds the default view stack and optionally pushes extra views on
// top of it. All of this happens in one main-loop iteration, so a previously
// queued view can never end up on top of the reset stack.
func uiReset(ctx *ntcontext, extra ...ViewFunc) {
	uiDo(func() {
		resetUI(ctx)
		for _, v := range extra {
			ctx.views.Push(v)
		}
		rebuildUI(ctx)
	})
}

func uiShowConnecting(ctx *ntcontext) {
	uiDo(func() {
		// The connection may have completed before this callback ran. In that
		// case the reset that follows will show the real UI, and pushing the
		// connecting view here would only leave it stuck on screen.
		if ctx.paClient.Connected() {
			return
		}
		ctx.views = NewViewStack()
		ctx.views.Push(connectView)
		rebuildUI(ctx)
	})
}

func newViewBox() *gtk.Box {
	box := gtk.NewBox(gtk.OrientationVertical, 18)
	box.SetMarginStart(18)
	box.SetMarginEnd(18)
	box.SetMarginTop(18)
	box.SetMarginBottom(18)
	return box
}

func newCard(spacing int) *gtk.Box {
	box := gtk.NewBox(gtk.OrientationVertical, spacing)
	box.AddCSSClass("card")
	box.SetMarginStart(14)
	box.SetMarginEnd(14)
	box.SetMarginTop(14)
	box.SetMarginBottom(14)
	return box
}

func newLabel(text, class string) *gtk.Label {
	l := gtk.NewLabel(text)
	l.SetHAlign(gtk.AlignStart)
	l.SetXAlign(0)
	l.SetWrap(true)
	if class != "" {
		l.AddCSSClass(class)
	}
	return l
}

func centeredLabel(text string) *gtk.Label {
	l := gtk.NewLabel(text)
	l.SetHAlign(gtk.AlignCenter)
	l.SetJustify(gtk.JustifyCenter)
	l.SetWrap(true)
	return l
}

func sectionTitle(text string) *gtk.Label {
	return newLabel(text, "title-4")
}

func statusCard(ctx *ntcontext) gtk.Widgetter {
	title, detail, iconName, class := filterStatus(ctx)

	box := gtk.NewBox(gtk.OrientationHorizontal, 14)
	box.AddCSSClass("card")
	box.SetMarginStart(16)
	box.SetMarginEnd(16)
	box.SetMarginTop(16)
	box.SetMarginBottom(16)

	icon := gtk.NewImageFromIconName(iconName)
	icon.SetPixelSize(36)
	icon.SetVAlign(gtk.AlignCenter)
	if class != "" {
		icon.AddCSSClass(class)
	}
	box.Append(icon)

	text := gtk.NewBox(gtk.OrientationVertical, 2)
	text.SetHExpand(true)
	text.SetVAlign(gtk.AlignCenter)
	titleLabel := newLabel(title, "title-3")
	if class != "" {
		titleLabel.AddCSSClass(class)
	}
	text.Append(titleLabel)
	text.Append(newLabel(detail, "dim-label"))
	box.Append(text)

	return box
}

func filterStatus(ctx *ntcontext) (title, detail, icon, class string) {
	switch ctx.noiseSupressorState {
	case loaded:
		if ctx.virtualDeviceInUse {
			return "Filtering active", "The virtual device is currently in use.", "object-select-symbolic", "success"
		}
		return "Filtering active", "The filter is loaded. Select the virtual device in your application.", "object-select-symbolic", "success"
	case inconsistent:
		return "Inconsistent state", "Unload the filter(s) before loading again.", "dialog-warning-symbolic", "warning"
	default:
		_, inpOk := inputSelection(ctx)
		_, outOk := outputSelection(ctx)
		if validConfiguration(ctx, inpOk, outOk) {
			return "Filtering inactive", "Choose Load filter(s) to start denoising.", "media-playback-pause-symbolic", ""
		}
		return "Filtering unconfigured", "Enable a filter and select a device below to get started.", "dialog-information-symbolic", ""
	}
}

func noticeCard(text, iconName, class string) gtk.Widgetter {
	box := gtk.NewBox(gtk.OrientationHorizontal, 12)
	box.AddCSSClass("card")
	box.SetMarginStart(14)
	box.SetMarginEnd(14)
	box.SetMarginTop(14)
	box.SetMarginBottom(14)

	icon := gtk.NewImageFromIconName(iconName)
	icon.SetPixelSize(24)
	icon.SetVAlign(gtk.AlignCenter)
	if class != "" {
		icon.AddCSSClass(class)
	}
	box.Append(icon)

	l := newLabel(text, "")
	l.SetVAlign(gtk.AlignCenter)
	l.SetHExpand(true)
	box.Append(l)
	return box
}

func settingsCard(ctx *ntcontext) gtk.Widgetter {
	box := gtk.NewBox(gtk.OrientationVertical, 10)
	box.Append(sectionTitle("Settings"))

	list := gtk.NewListBox()
	list.SetSelectionMode(gtk.SelectionNone)
	list.AddCSSClass("boxed-list")

	monSwitch := gtk.NewSwitch()
	monSwitch.SetActive(ctx.config.DisplayMonitorSources)
	monSwitch.SetVAlign(gtk.AlignCenter)
	monSwitch.ConnectStateSet(func(state bool) bool {
		ctx.config.DisplayMonitorSources = state
		go writeConfig(ctx.config)
		refreshUI(ctx)
		return false
	})
	list.Append(settingRow("Display monitor sources", "Show loopback and monitor devices in the lists.", monSwitch))

	inputSwitch := gtk.NewSwitch()
	inputSwitch.SetActive(ctx.config.FilterInput)
	inputSwitch.SetVAlign(gtk.AlignCenter)
	inputSwitch.ConnectStateSet(func(state bool) bool {
		ctx.config.FilterInput = state
		go writeConfig(ctx.config)
		go func() {
			ctx.noiseSupressorState, ctx.virtualDeviceInUse = supressorState(ctx)
			refreshUI(ctx)
		}()
		return false
	})
	list.Append(settingRow("Filter microphone", "Create a denoised virtual microphone.", inputSwitch))

	outputSwitch := gtk.NewSwitch()
	outputSwitch.SetActive(ctx.config.FilterOutput)
	outputSwitch.SetVAlign(gtk.AlignCenter)
	outputSwitch.ConnectStateSet(func(state bool) bool {
		ctx.config.FilterOutput = state
		go writeConfig(ctx.config)
		go func() {
			ctx.noiseSupressorState, ctx.virtualDeviceInUse = supressorState(ctx)
			refreshUI(ctx)
		}()
		return false
	})
	list.Append(settingRow("Filter headphones", "Create a denoised virtual output device.", outputSwitch))

	box.Append(list)
	box.Append(thresholdCard(ctx))
	return box
}

func settingRow(title, subtitle string, control gtk.Widgetter) *gtk.ListBoxRow {
	row := gtk.NewListBoxRow()
	row.SetSelectable(false)
	row.SetActivatable(false)

	box := gtk.NewBox(gtk.OrientationHorizontal, 12)
	box.SetMarginStart(12)
	box.SetMarginEnd(12)
	box.SetMarginTop(10)
	box.SetMarginBottom(10)

	text := gtk.NewBox(gtk.OrientationVertical, 2)
	text.SetHExpand(true)
	text.SetVAlign(gtk.AlignCenter)
	text.Append(newLabel(title, ""))
	text.Append(newLabel(subtitle, "dim-label"))
	box.Append(text)

	gtk.BaseWidget(control).SetVAlign(gtk.AlignCenter)
	box.Append(control)

	row.SetChild(box)
	return row
}

func thresholdCard(ctx *ntcontext) gtk.Widgetter {
	box := gtk.NewBox(gtk.OrientationVertical, 6)

	row := gtk.NewBox(gtk.OrientationHorizontal, 8)
	title := newLabel("Voice activation threshold", "")
	title.SetHExpand(true)
	row.Append(title)

	valueLabel := gtk.NewLabel(fmt.Sprintf("%d%%", ctx.config.Threshold))
	valueLabel.AddCSSClass("dim-label")
	row.Append(valueLabel)
	box.Append(row)

	scale := gtk.NewScaleWithRange(gtk.OrientationHorizontal, 0, 95, 1)
	scale.SetValue(float64(ctx.config.Threshold))
	scale.SetDrawValue(false)
	scale.SetDigits(0)
	scale.SetHExpand(true)
	scale.SetTooltipText("If you have a decent microphone, you can usually turn this all the way up.")
	scale.AddMark(0, gtk.PosBottom, "Off")
	scale.AddMark(95, gtk.PosBottom, "Max")
	reloadLabel := newLabel("Reload the filter(s) to apply this change.", "warning")
	reloadLabel.SetVisible(ctx.reloadRequired)

	scale.ConnectValueChanged(func() {
		ctx.config.Threshold = int(scale.Value())
		valueLabel.SetText(fmt.Sprintf("%d%%", ctx.config.Threshold))
		go writeConfig(ctx.config)
		ctx.reloadRequired = true
		reloadLabel.SetVisible(true)
	})
	box.Append(scale)
	box.Append(reloadLabel)

	return box
}

func mainView(ctx *ntcontext) gtk.Widgetter {
	box := newViewBox()

	box.Append(statusCard(ctx))

	if ctx.update.available && !ctx.update.triggered {
		row := gtk.NewBox(gtk.OrientationHorizontal, 12)
		row.AddCSSClass("card")
		row.SetMarginStart(14)
		row.SetMarginEnd(14)
		row.SetMarginTop(14)
		row.SetMarginBottom(14)

		l := newLabel("Update available: "+ctx.update.serverVersion, "")
		l.SetHExpand(true)
		l.SetVAlign(gtk.AlignCenter)
		row.Append(l)

		btn := gtk.NewButtonWithLabel("Update")
		btn.AddCSSClass("suggested-action")
		btn.ConnectClicked(func() {
			ctx.update.triggered = true
			go update(ctx)
			refreshUI(ctx)
		})
		row.Append(btn)
		box.Append(row)
	}

	if ctx.update.triggered {
		box.Append(noticeCard(ctx.update.updatingText, "software-update-available-symbolic", ""))
	}

	box.Append(settingsCard(ctx))

	if ctx.config.FilterInput {
		box.Append(deviceSelector(ctx, "Microphone", "Select the microphone to denoise.", &ctx.inputList))
	}

	if ctx.config.FilterOutput {
		box.Append(deviceSelector(ctx, "Headphones", "Select the output device to denoise.", &ctx.outputList))
	}

	buttonRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	buttonRow.SetHomogeneous(true)
	box.Append(buttonRow)

	if ctx.noiseSupressorState != unloaded {
		unloadBtn := gtk.NewButtonWithLabel("Unload Filter(s)")
		unloadBtn.AddCSSClass("destructive-action")
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
		loadBtn.AddCSSClass("suggested-action")
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
	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.Append(sectionTitle(title))
	box.Append(newLabel(hint, "dim-label"))

	list := gtk.NewListBox()
	list.SetSelectionMode(gtk.SelectionNone)
	list.AddCSSClass("boxed-list")

	var group *gtk.CheckButton
	shown := 0
	for i := range *devices {
		el := &(*devices)[i]

		if el.isMonitor && !ctx.config.DisplayMonitorSources {
			continue
		}
		shown++

		cb := gtk.NewCheckButton()
		if group == nil {
			group = cb
		} else {
			cb.SetGroup(group)
		}
		cb.SetActive(el.checked)
		cb.SetVAlign(gtk.AlignCenter)

		row := gtk.NewListBoxRow()
		row.SetSelectable(false)
		row.SetActivatable(false)

		content := gtk.NewBox(gtk.OrientationHorizontal, 12)
		content.SetMarginStart(12)
		content.SetMarginEnd(12)
		content.SetMarginTop(8)
		content.SetMarginBottom(8)
		content.Append(cb)

		name := newLabel(el.Name, "")
		name.SetHExpand(true)
		name.SetVAlign(gtk.AlignCenter)
		content.Append(name)

		if !el.dynamicLatency {
			warn := gtk.NewImageFromIconName("dialog-warning-symbolic")
			warn.SetTooltipText("This device does not support dynamic latency and may cause issues.")
			warn.SetVAlign(gtk.AlignCenter)
			content.Append(warn)
		}

		cb.ConnectToggled(func() {
			if !cb.Active() {
				return
			}
			ensureOnlyOneInputSelected(devices, el)
			refreshUI(ctx)
		})

		row.SetChild(content)
		list.Append(row)
	}

	if shown == 0 {
		row := gtk.NewListBoxRow()
		row.SetSelectable(false)
		row.SetActivatable(false)
		l := newLabel("No compatible devices found.", "dim-label")
		l.SetMarginStart(12)
		l.SetMarginEnd(12)
		l.SetMarginTop(10)
		l.SetMarginBottom(10)
		row.SetChild(l)
		list.Append(row)
	}

	box.Append(list)
	return box
}

func uiUnloadFilters(ctx *ntcontext) {
	uiPush(ctx, loadingView)
	if err := unloadSupressor(ctx); err != nil {
		log.Println(err)
		uiReset(ctx, makeErrorView(ctx, fmt.Sprintf("Could not unload filter(s): %v", err)))
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
		uiReset(ctx, makeErrorView(ctx, err.Error()))
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
		uiReset(ctx, makeErrorView(ctx, fmt.Sprintf("Could not load filter(s): %v", err)))
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

func busyView(text string) gtk.Widgetter {
	box := gtk.NewBox(gtk.OrientationVertical, 12)
	box.SetHAlign(gtk.AlignCenter)
	box.SetVAlign(gtk.AlignCenter)
	box.SetVExpand(true)
	box.SetMarginStart(24)
	box.SetMarginEnd(24)
	box.SetMarginTop(24)
	box.SetMarginBottom(24)

	spinner := gtk.NewSpinner()
	spinner.SetHAlign(gtk.AlignCenter)
	spinner.Start()
	box.Append(spinner)

	l := centeredLabel(text)
	l.AddCSSClass("title-4")
	box.Append(l)
	return box
}

func loadingView(ctx *ntcontext) gtk.Widgetter {
	return busyView("Working...")
}

func licenseView(ctx *ntcontext) gtk.Widgetter {
	box := newViewBox()
	box.Append(centeredLabel(notice))

	tv := gtk.NewTextView()
	tv.SetEditable(false)
	tv.SetWrapMode(gtk.WrapWord)
	tv.SetMonospace(true)
	tv.SetTopMargin(8)
	tv.SetBottomMargin(8)
	tv.SetLeftMargin(8)
	tv.SetRightMargin(8)
	tv.Buffer().SetText(licenseString)
	tv.SetVExpand(true)
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
	return busyView("Connecting to PulseAudio...")
}

func capabilitiesView(ctx *ntcontext) gtk.Widgetter {
	box := newViewBox()
	box.Append(centeredLabel("This program does not have the capabilities to function properly."))
	box.Append(centeredLabel("We require CAP_SYS_RESOURCE. If that doesn't mean anything to you, don't worry. I'll fix it for you."))
	if ctx.capsMismatch {
		box.Append(noticeCard("The file has CAP_SYS_RESOURCE but this process doesn't. Check if your filesystem has nosuid set or see the troubleshooting page.",
			"dialog-warning-symbolic", "warning"))
	}
	btn := gtk.NewButtonWithLabel("Grant capability (requires root)")
	btn.SetHAlign(gtk.AlignCenter)
	btn.AddCSSClass("suggested-action")
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
		quitBtn.AddCSSClass("destructive-action")
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
		confirmBtn.AddCSSClass("destructive-action")
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
