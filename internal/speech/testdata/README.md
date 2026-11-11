# Speech compatibility fixtures

These fixtures were generated from Margin's accepted MIT Python implementation
on 2026-11-09, using the unchanged Kokoro v1.0 model, voice archive, bundled
pronunciation assets, and ONNX Runtime 1.31.1. The sentences are synthetic test
text, not saved user readings.

- `python_pronunciation.json` records normalized text and exact US/GB phonemes.
- `python_audio.json` describes two short speech references; the `.f32` files
  contain mono little-endian float32 samples at 24 kHz.

The native integration tests compare exact phonemes and sample counts, then
signal correlation and relative error. Kokoro uses random noise internally;
even repeated runs of the Python implementation are not bit-identical.

To include the native checks when running `go test ./internal/speech`, set
`MARGIN_ORT_LIBRARY` to a compatible ONNX Runtime shared library and
`MARGIN_TEST_MODEL_DIR` to the Kokoro model directory. Other tests work without
native model files. Neither fixture generation nor Python is needed at runtime.
