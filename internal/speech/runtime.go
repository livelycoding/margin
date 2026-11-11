package speech

import (
	"context"
	"errors"
	"fmt"
	ort "github.com/yalue/onnxruntime_go"
	"sync"
)

// The wrapper owns one process-wide ORT environment. Reference counting lets
// multiple engines coexist without unloading native code underneath a session.
var runtimeState struct {
	sync.Mutex
	refs int
	path string
}

func acquireRuntime(path string) error {
	runtimeState.Lock()
	defer runtimeState.Unlock()
	if runtimeState.refs > 0 {
		if runtimeState.path != path {
			return errors.New("ONNX Runtime is already loaded from a different library path")
		}
		runtimeState.refs++
		return nil
	}
	ort.SetSharedLibraryPath(path)
	if err := ort.InitializeEnvironment(ort.WithLogLevelError()); err != nil {
		return fmt.Errorf("load ONNX Runtime: %w", err)
	}
	if err := ort.DisableTelemetry(); err != nil {
		ort.DestroyEnvironment()
		return err
	}
	runtimeState.path = path
	runtimeState.refs = 1
	return nil
}
func releaseRuntime() error {
	runtimeState.Lock()
	defer runtimeState.Unlock()
	runtimeState.refs--
	if runtimeState.refs == 0 {
		runtimeState.path = ""
		return ort.DestroyEnvironment()
	}
	return nil
}
func newSession(path string, inputs, outputs []string, threads int) (*ort.DynamicAdvancedSession, error) {
	options, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer options.Destroy()
	if err := options.SetIntraOpNumThreads(threads); err != nil {
		return nil, err
	}
	if err := options.SetInterOpNumThreads(1); err != nil {
		return nil, err
	}
	if err := options.SetExecutionMode(ort.ExecutionModeSequential); err != nil {
		return nil, err
	}
	return ort.NewDynamicAdvancedSession(path, inputs, outputs, options)
}
func runFloat(ctx context.Context, session *ort.DynamicAdvancedSession, inputs []ort.Value) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options, err := ort.NewRunOptions()
	if err != nil {
		return nil, err
	}
	defer options.Destroy()
	finished, watcherDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			_ = options.Terminate()
		case <-finished:
		}
	}()
	outputs := []ort.Value{nil}
	err = session.RunWithOptions(inputs, outputs, options)
	close(finished)
	<-watcherDone
	if outputs[0] != nil {
		defer outputs[0].Destroy()
	}
	if cancelErr := ctx.Err(); cancelErr != nil {
		return nil, cancelErr
	}
	if err != nil {
		return nil, err
	}
	result, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, errors.New("Speech model returned a non-float tensor")
	}
	return append([]float32(nil), result.GetData()...), nil
}
