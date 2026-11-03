package main

// #include "native.h"
import "C"

import "runtime"

func main() {
	runtime.Breakpoint()
	result := C.native_add(7)
	runtime.KeepAlive(result)
	runtime.Breakpoint()
	callback := C.native_callback(4)
	runtime.KeepAlive(callback)
}

//export goDouble
func goDouble(value C.int) C.int {
	result := value * 2
	return result
}
