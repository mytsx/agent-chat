//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// enableFullScreenPrimary adds NSWindowCollectionBehaviorFullScreenPrimary to every
// application window so the green title-bar button performs NATIVE macOS fullscreen
// instead of a plain zoom/maximise. Wails only sets this collection behavior when the
// app is configured to START in fullscreen, so a normally-started window's green
// button just zooms and there is no fullscreen affordance. Dispatched to the main
// thread because AppKit window mutations must not happen on a background goroutine.
static void enableFullScreenPrimary() {
    dispatch_async(dispatch_get_main_queue(), ^{
        for (NSWindow *w in [NSApp windows]) {
            [w setCollectionBehavior:[w collectionBehavior] | NSWindowCollectionBehaviorFullScreenPrimary];
        }
    });
}
*/
import "C"

// enableNativeFullscreen makes the macOS green title-bar button enter native
// fullscreen. Called once the window exists (domReady). See the C comment for why
// Wails leaves the button doing zoom by default.
func enableNativeFullscreen() {
	C.enableFullScreenPrimary()
}
