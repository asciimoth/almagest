// Package experimental contains node classes that intentionally cross current
// Almagest abstraction boundaries.
//
// Experimental classes are not registered by the library. A host must create
// them explicitly and pass them in BuildEngine's extra node-class list. Their
// class names start with X so saved graphs make the experimental contract
// visible.
package experimentalnodes
