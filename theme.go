// This file is part of the program "NoiseTorch-ng".
// Please see the LICENSE file for copyright information.

package main

/*
#cgo pkg-config: gtk4
#include <gtk/gtk.h>

static void nt_set_interface_color_scheme(guintptr settings_ptr, gint scheme) {
	GtkSettings *settings = (GtkSettings *)settings_ptr;
	g_object_set(G_OBJECT(settings), "gtk-interface-color-scheme", scheme, NULL);
}
*/
import "C"

import (
	"log"
	"os"
	"runtime"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

const (
	gtkInterfaceColorSchemeDark  = 2
	gtkInterfaceColorSchemeLight = 3
)

// applySystemTheme propagates the desktop color scheme to GTK.
//
// On NixOS the binary is started through a capability wrapper. Processes that
// gain capabilities are non-dumpable, so xdg-desktop-portal refuses to serve
// the settings portal and GTK falls back to its light defaults instead of the
// user's dark preference. The theme itself is still read from GSettings and
// gtk-4.0/settings.ini, so only the color scheme needs to be restored here.
func applySystemTheme() {
	if os.Getenv("GTK_THEME") != "" {
		return
	}

	schemaSource := gio.SettingsSchemaSourceGetDefault()
	if schemaSource == nil {
		return
	}
	schema := schemaSource.Lookup("org.gnome.desktop.interface", true)
	if schema == nil || !schema.HasKey("color-scheme") {
		return
	}
	gtkSettings := gtk.SettingsGetDefault()
	if gtkSettings == nil {
		return
	}
	if gtkSettings.PropertyType("gtk-interface-color-scheme") == coreglib.TypeInvalid {
		return
	}

	settings := gio.NewSettings("org.gnome.desktop.interface")
	colorScheme := settings.String("color-scheme")

	var scheme C.gint
	switch colorScheme {
	case "prefer-dark":
		scheme = C.gint(gtkInterfaceColorSchemeDark)
	case "prefer-light":
		scheme = C.gint(gtkInterfaceColorSchemeLight)
	default:
		// "default" means GTK should decide; leave it alone so that themes
		// configured through gtk-4.0/settings.ini are not overridden.
		return
	}

	native := coreglib.InternObject(gtkSettings).Native()
	C.nt_set_interface_color_scheme(C.guintptr(native), scheme)
	runtime.KeepAlive(gtkSettings)

	log.Printf("Applied desktop color scheme: %s\n", colorScheme)
}
