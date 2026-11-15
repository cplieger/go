// Package app is an app's own wire package, as an app that draws charts
// writes it: payloads that carry chartdata's frames beside its own types.
package app

import "github.com/cplieger/go/chartdata"

// Event is the app's own event, whose bare name chartdata's wire leaves free.
type Event struct {
	Kind string `json:"kind"`
}

// Dashboard is a payload of the app's API holding frames it built.
type Dashboard struct {
	Title string                    `json:"title"`
	Runs  chartdata.CategoriesFrame `json:"runs"`
	Waits chartdata.SeriesFrame     `json:"waits"`
}
