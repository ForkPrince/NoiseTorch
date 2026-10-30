// This file is part of the program "NoiseTorch-ng".
// Please see the LICENSE file for copyright information.

package main

import (
	"log"

	"github.com/aarzilli/nucular"
)

type ViewFunc func(ctx *ntcontext, w *nucular.Window)

type ViewStack struct {
	items []ViewFunc
}

func NewViewStack() *ViewStack {
	return &ViewStack{make([]ViewFunc, 0)}
}

func (v *ViewStack) Push(f ViewFunc) {
	v.items = append(v.items, f)
}

func (v *ViewStack) Pop() ViewFunc {
	if len(v.items) == 0 {
		log.Println("Tried to Pop an empty ViewStack")
		return nil
	}

	item := v.items[len(v.items)-1]

	// The last item gets removed
	v.items = v.items[:len(v.items)-1]

	return item
}

func (v *ViewStack) Peek() ViewFunc {
	if len(v.items) == 0 {
		return nil
	}

	return v.items[len(v.items)-1]
}
