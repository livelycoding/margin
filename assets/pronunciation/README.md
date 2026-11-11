# English pronunciation assets

These files are local ONNX conversions of PeterReid's American and British
English grapheme-to-phoneme models, each published under Apache License 2.0.
The original models were trained on Misaki's pronunciation dictionaries.

Margin contributors converted the model format and extracted small tokenizer
configuration files. The weights were not retrained. The exact upstream
revisions, original file hashes, output hashes, export environment, and license
evidence are recorded in `PROVENANCE.json`. The Apache license is included here.

`tools/export_g2p.py` rebuilds the ONNX files from the original safetensors
weights. Torch, Transformers and ONNX are developer tools used during this
conversion; Margin's running application uses Go and ONNX Runtime; Python is not required.
No Python code from a model repository is executed during conversion.

The converted models matched the original models' decoded output on 112 test
words per accent. This checks conversion fidelity, not pronunciation accuracy.
Unfamiliar names can still be mispronounced. The neural helper reports unsupported
words and decoding that fails to terminate. Margin's text frontend then spells
supported Latin-letter words aloud and records a warning; it does not silently
omit them. Other unsupported text fails visibly.

## Dictionaries and token table

`lexicon-us-en.txt.gz`, `lexicon-gb-en.txt.gz`, and `tokens.txt` are selected from
the official `kokoro-multi-lang-v1_0.tar.bz2` archive published by Sherpa-ONNX.
Only those three data files are used. In particular, Margin does not include or
load the archive's eSpeak data, speech libraries, model, or voice table.

The [upstream lexicon converter](https://github.com/k2-fsa/sherpa-onnx/blob/v2.0.1/scripts/kokoro/v1.0/generate_lexicon_en.py)
combines Misaki's US/GB gold and silver dictionaries and selects default
pronunciations. The [upstream build script](https://github.com/k2-fsa/sherpa-onnx/blob/v2.0.1/scripts/kokoro/v1.0/run.sh)
identifies Misaki as the data source. Misaki publishes these dictionaries under
[Apache License 2.0](https://github.com/hexgrad/misaki/blob/fba1236595f2d2bf21d414ba6e57d25256afada3/LICENSE).
The exact Misaki revision used to make the archive was not recorded by upstream;
the archive hash and raw file hashes pin the data actually included here.

The token table comes from the Apache-licensed Kokoro-82M model vocabulary, as
shown by the [upstream token generator](https://github.com/k2-fsa/sherpa-onnx/blob/v2.0.1/scripts/kokoro/v1.0/generate_tokens.py).
These data retain their Apache license; Margin's application license does not
replace it. Upstream conversion scripts are by Xiaomi Corp., author Fangjun
Kuang; they are referenced here, not included in the runtime.

`tools/rebuild_pronunciation_lexicons.py` verifies the original archive and reads
only the three allowlisted regular files, then compresses the lexicons with
gzip level 9, timestamp 0, no filename and OS byte 255. The source and output
hashes and the archive URL are in `PROVENANCE.json`. Rebuilding was verified with
Python 3.12.14 and zlib 1.3.2. No archive extraction command is needed.

Upstream sources:

- https://huggingface.co/PeterReid/graphemes_to_phonemes_en_us
- https://huggingface.co/PeterReid/graphemes_to_phonemes_en_gb
- https://github.com/hexgrad/misaki
