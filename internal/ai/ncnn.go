package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// NCNNLoader implements Inferencer using the NCNN C API via dlopen/dlsym.
// All C function calls go through purego (no CGO), making this usable
// with CGO_ENABLED=0 on all platforms supported by purego Tier 1 (linux amd64/arm64).
type NCNNLoader struct {
	handle    uintptr
	available bool
	mu        sync.Mutex

	paramPath string
	binPath   string

	// Resolved C function wrappers (registered via purego.RegisterFunc from dlsym addresses)
	ncnnCreateNet             func() unsafe.Pointer
	ncnnDestroyNet            func(net unsafe.Pointer)
	ncnnNetLoadParam          func(net unsafe.Pointer, path string) int
	ncnnNetLoadModel          func(net unsafe.Pointer, path string) int
	ncnnExtractorCreate       func(net unsafe.Pointer) unsafe.Pointer
	ncnnExtractorDestroy      func(ex unsafe.Pointer)
	ncnnExtractorInput        func(ex unsafe.Pointer, name string, mat unsafe.Pointer) int
	ncnnExtractorExtract      func(ex unsafe.Pointer, name string, mat *unsafe.Pointer) int
	ncnnMatCreate3D           func(w, h, c int, alloc unsafe.Pointer) unsafe.Pointer
	ncnnMatDestroy            func(mat unsafe.Pointer)
	ncnnMatGetData            func(mat unsafe.Pointer) unsafe.Pointer
	ncnnMatGetW               func(mat unsafe.Pointer) int
	ncnnMatGetH               func(mat unsafe.Pointer) int
	ncnnMatGetC               func(mat unsafe.Pointer) int
	ncnnMatGetDims            func(mat unsafe.Pointer) int
	ncnnAllocatorCreatePool   func() unsafe.Pointer
	ncnnAllocatorDestroy      func(alloc unsafe.Pointer)
}

// compiler check that NCNNLoader satisfies Inferencer
var _ Inferencer = (*NCNNLoader)(nil)

// DefaultSearchPaths is the list of library paths tried by Load() for libncnn.so.
// It covers common locations on Linux including ARM/Raspberry Pi paths.
var DefaultSearchPaths = []string{
	"libncnn.so",
	"./libncnn.so",
	"/usr/lib/libncnn.so",
	"/usr/local/lib/libncnn.so",
	"/usr/lib/aarch64-linux-gnu/libncnn.so",
	"/usr/lib/arm-linux-gnueabihf/libncnn.so",
}

// Load attempts to open libncnn.so from each search path using purego.Dlopen.
// On success it resolves all required C function symbols via dlsym.
// Non-fatal: if no path works, available is set to false and an error logged.
func (l *NCNNLoader) Load(searchPaths []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if searchPaths == nil {
		searchPaths = DefaultSearchPaths
	}

	var handle uintptr
	for _, path := range searchPaths {
		h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err == nil {
			handle = h
			slog.Info("NCNN: loaded shared library", "path", path)
			break
		}
		slog.Debug("NCNN: library not found at path", "path", path, "error", err)
	}

	if handle == 0 {
		l.available = false
		slog.Warn("NCNN: libncnn.so not found — AI inference disabled")
		return fmt.Errorf("NCNN: libncnn.so not found in any search path")
	}

	if err := l.resolveSymbols(handle); err != nil {
		l.available = false
		purego.Dlclose(handle)
		return err
	}

	l.handle = handle
	l.available = true
	slog.Info("NCNN: loaded successfully with all required symbols")
	return nil
}

// Unload closes the shared library handle and marks the loader as unavailable.
func (l *NCNNLoader) Unload() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.handle != 0 {
		if err := purego.Dlclose(l.handle); err != nil {
			return fmt.Errorf("NCNN: dlclose failed: %w", err)
		}
		l.handle = 0
	}
	l.available = false
	return nil
}

// IsAvailable reports whether the NCNN shared library was loaded successfully.
func (l *NCNNLoader) IsAvailable() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.available
}

// SetModelPaths stores the param and bin file paths for subsequent Infer calls.
func (l *NCNNLoader) SetModelPaths(paramPath, binPath string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paramPath = paramPath
	l.binPath = binPath
}

// LoadModel creates a new ncnn_net, loads the param and bin model files,
// and returns the net handle. The caller is responsible for destroying
// the returned net via ncnnDestroyNet (or using the loader's Infer method).
func (l *NCNNLoader) LoadModel(paramPath, binPath string) (unsafe.Pointer, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if !l.available {
		return nil, errors.New("NCNN: not available")
	}

	net := l.ncnnCreateNet()
	if net == nil {
		return nil, errors.New("NCNN: ncnn_net_create failed")
	}

	if ret := l.ncnnNetLoadParam(net, paramPath); ret != 0 {
		l.ncnnDestroyNet(net)
		return nil, fmt.Errorf("NCNN: load param failed (code=%d): %s", ret, paramPath)
	}

	if ret := l.ncnnNetLoadModel(net, binPath); ret != 0 {
		l.ncnnDestroyNet(net)
		return nil, fmt.Errorf("NCNN: load model failed (code=%d): %s", ret, binPath)
	}

	return net, nil
}

// CreateExtractor creates a new ncnn_extractor from the given net handle.
// The caller must destroy the returned extractor via ncnnExtractorDestroy.
func (l *NCNNLoader) CreateExtractor(net unsafe.Pointer) (unsafe.Pointer, error) {
	ex := l.ncnnExtractorCreate(net)
	if ex == nil {
		return nil, errors.New("NCNN: ncnn_extractor_create failed")
	}
	return ex, nil
}

// Infer implements Inferencer.Infer.
// It runs the full NCNN inference pipeline:
//  1. Creates a net and loads the model (from previously stored param/bin paths)
//  2. Creates an extractor
//  3. Creates an input mat from the tensor data
//  4. Sets the input blob on the extractor
//  5. Runs inference via extract (output blob name "output")
//  6. Reads and returns the output data as a 2D float32 slice
//  7. Cleans up all C resources
//
// The output is reshaped by channel: each inner slice corresponds to one
// output channel of size W*H. For single-channel outputs a single slice is returned.
// Context cancellation is checked before each blocking C operation.
func (l *NCNNLoader) Infer(ctx context.Context, tensor []float32, dims []int64) ([][]float32, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if !l.available {
		return nil, errors.New("NCNN: not available")
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("NCNN: context cancelled: %w", err)
	}

	// Parse dimensions: NCNN mat uses (w, h, c) order.
	// Common input layouts: (N, C, H, W) or (C, H, W).
	if len(dims) < 3 {
		return nil, fmt.Errorf("NCNN: expected at least 3 dims, got %d", len(dims))
	}
	c := int(dims[len(dims)-3])
	h := int(dims[len(dims)-2])
	w := int(dims[len(dims)-1])
	elems := w * h * c

	if len(tensor) < elems {
		return nil, fmt.Errorf("NCNN: tensor too small: got %d elements, need %d", len(tensor), elems)
	}

	// Track allocated resources for deferred cleanup (reverse order).
	var cleanups []func()
	defer func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}()

	// --- Step 1: Create net ---
	net := l.ncnnCreateNet()
	if net == nil {
		return nil, errors.New("NCNN: ncnn_net_create failed")
	}
	cleanups = append(cleanups, func() { l.ncnnDestroyNet(net) })

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("NCNN: context cancelled before param load: %w", err)
	}

	// --- Step 2: Load param (I/O, can block) ---
	if ret := l.ncnnNetLoadParam(net, l.paramPath); ret != 0 {
		return nil, fmt.Errorf("NCNN: load param failed (code=%d): %s", ret, l.paramPath)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("NCNN: context cancelled before model load: %w", err)
	}

	// --- Step 3: Load model (I/O, can block) ---
	if ret := l.ncnnNetLoadModel(net, l.binPath); ret != 0 {
		return nil, fmt.Errorf("NCNN: load model failed (code=%d): %s", ret, l.binPath)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("NCNN: context cancelled before extractor: %w", err)
	}

	// --- Step 4: Create optional pool allocator ---
	var alloc unsafe.Pointer
	if l.ncnnAllocatorCreatePool != nil {
		alloc = l.ncnnAllocatorCreatePool()
	}
	if alloc != nil {
		cleanups = append(cleanups, func() { l.ncnnAllocatorDestroy(alloc) })
	}

	// --- Step 5: Create input mat ---
	inputMat := l.ncnnMatCreate3D(w, h, c, alloc)
	if inputMat == nil {
		return nil, errors.New("NCNN: ncnn_mat_create_3d failed")
	}
	cleanups = append(cleanups, func() { l.ncnnMatDestroy(inputMat) })

	// Copy tensor data into the C-allocated mat (safe: C heap memory, GC won't move it).
	matData := l.ncnnMatGetData(inputMat)
	if matData == nil {
		return nil, errors.New("NCNN: ncnn_mat_get_data returned nil")
	}
	dst := unsafe.Slice((*float32)(matData), elems)
	copy(dst, tensor[:elems])

	// --- Step 6: Create extractor ---
	ex := l.ncnnExtractorCreate(net)
	if ex == nil {
		return nil, errors.New("NCNN: ncnn_extractor_create failed")
	}
	cleanups = append(cleanups, func() { l.ncnnExtractorDestroy(ex) })

	// --- Step 7: Set input blob ---
	if ret := l.ncnnExtractorInput(ex, "data", inputMat); ret != 0 {
		return nil, fmt.Errorf("NCNN: extractor input failed (code=%d)", ret)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("NCNN: context cancelled before extract: %w", err)
	}

	// --- Step 8: Run inference (compute, can block) ---
	var outMat unsafe.Pointer
	if ret := l.ncnnExtractorExtract(ex, "output", &outMat); ret != 0 {
		return nil, fmt.Errorf("NCNN: extractor extract failed (code=%d)", ret)
	}
	cleanups = append(cleanups, func() { l.ncnnMatDestroy(outMat) })

	// --- Step 9: Read output data ---
	outData := l.ncnnMatGetData(outMat)
	if outData == nil {
		return nil, errors.New("NCNN: output mat get_data returned nil")
	}
	outW := l.ncnnMatGetW(outMat)
	outH := l.ncnnMatGetH(outMat)
	outC := l.ncnnMatGetC(outMat)
	totalOut := outW * outH * outC

	if totalOut == 0 {
		return nil, errors.New("NCNN: output mat is empty")
	}

	outSlice := unsafe.Slice((*float32)(outData), totalOut)

	// Copy output data to Go heap so it survives C resource destruction.
	flatCopy := make([]float32, totalOut)
	copy(flatCopy, outSlice)

	// Reshape by channel for natural postprocessor consumption.
	result := make([][]float32, 0, max(outC, 1))
	if outC > 1 && outW*outH > 0 {
		chSize := outW * outH
		for i := 0; i < outC; i++ {
			row := make([]float32, chSize)
			copy(row, flatCopy[i*chSize:(i+1)*chSize])
			result = append(result, row)
		}
	} else {
		result = append(result, flatCopy)
	}

	return result, nil
}

// resolveSymbols looks up all required and optional C function symbols via Dlsym
// and registers them as Go-callable wrappers via RegisterFunc.
// Required symbol failures are fatal; optional ones (allocator) gracefully degrade.
func (l *NCNNLoader) resolveSymbols(handle uintptr) error {
	// Required net functions
	l.resolveRequired(&l.ncnnCreateNet, handle, "ncnn_net_create")
	l.resolveRequired(&l.ncnnDestroyNet, handle, "ncnn_net_destroy")
	l.resolveRequired(&l.ncnnNetLoadParam, handle, "ncnn_net_load_param")
	l.resolveRequired(&l.ncnnNetLoadModel, handle, "ncnn_net_load_model")

	// Required extractor functions
	l.resolveRequired(&l.ncnnExtractorCreate, handle, "ncnn_extractor_create")
	l.resolveRequired(&l.ncnnExtractorDestroy, handle, "ncnn_extractor_destroy")
	l.resolveRequired(&l.ncnnExtractorInput, handle, "ncnn_extractor_input")
	l.resolveRequired(&l.ncnnExtractorExtract, handle, "ncnn_extractor_extract")

	// Required mat functions
	l.resolveRequired(&l.ncnnMatCreate3D, handle, "ncnn_mat_create_3d")
	l.resolveRequired(&l.ncnnMatDestroy, handle, "ncnn_mat_destroy")
	l.resolveRequired(&l.ncnnMatGetData, handle, "ncnn_mat_get_data")
	l.resolveRequired(&l.ncnnMatGetW, handle, "ncnn_mat_get_w")
	l.resolveRequired(&l.ncnnMatGetH, handle, "ncnn_mat_get_h")
	l.resolveRequired(&l.ncnnMatGetC, handle, "ncnn_mat_get_c")
	l.resolveRequired(&l.ncnnMatGetDims, handle, "ncnn_mat_get_dims")

	// Optional: pool allocator (null allocator = default)
	l.resolveOptional(&l.ncnnAllocatorCreatePool, handle, "ncnn_allocator_create_pool_allocator")
	l.resolveOptional(&l.ncnnAllocatorDestroy, handle, "ncnn_allocator_destroy")

	return nil
}

// resolveRequired looks up a C function symbol and registers it.
// Panics from RegisterLibFunc are treated as fatal resolution failures.
func (l *NCNNLoader) resolveRequired(fptr any, handle uintptr, name string) {
	if err := l.resolve(fptr, handle, name); err != nil {
		slog.Error("NCNN: required symbol not found", "name", name)
		panic(fmt.Sprintf("NCNN: required symbol %s not found", name))
	}
}

// resolveOptional looks up an optional C function symbol.
// If the symbol is absent, the function pointer stays nil and a debug log is emitted.
func (l *NCNNLoader) resolveOptional(fptr any, handle uintptr, name string) {
	if err := l.resolve(fptr, handle, name); err != nil {
		slog.Debug("NCNN: optional symbol not found, skipping", "name", name)
	}
}

// resolve performs the actual Dlsym lookup and RegisterFunc registration.
func (l *NCNNLoader) resolve(fptr any, handle uintptr, name string) error {
	sym, err := purego.Dlsym(handle, name)
	if err != nil {
		return fmt.Errorf("dlsym %s: %w", name, err)
	}
	purego.RegisterFunc(fptr, sym)
	return nil
}
