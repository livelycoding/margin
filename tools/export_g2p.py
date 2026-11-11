#!/usr/bin/env python3
"""Rebuild the vendored English pronunciation ONNX models (developer tool).

This is not used at setup or runtime. Use Python 3.12 with CPU torch==2.5.1,
transformers==4.48.3, onnx==1.17.0, numpy==2.2.3 and onnxruntime==1.23.1.
Download the pinned repositories listed in assets/pronunciation/PROVENANCE.json
into INPUT/upstream-us and INPUT/upstream-gb, preserving config.json,
generation_config.json and model.safetensors. Then run:

    python tools/export_g2p.py --input INPUT --output assets/pronunciation

Only local safetensors weights are loaded; no repository Python is executed.
The ONNX graph implements the full BART forward pass, one pass per decoded
phoneme. Runtime inference and sequence limits live in g2p_onnx.py.
"""

import argparse
import hashlib
import json
from pathlib import Path


def export(source: Path, output: Path, accent: str):
    import onnx
    import torch
    from transformers import BartForConditionalGeneration

    class Forward(torch.nn.Module):
        def __init__(self, model):
            super().__init__()
            self.model = model

        def forward(self, input_ids, decoder_input_ids):
            return self.model(
                input_ids=input_ids,
                decoder_input_ids=decoder_input_ids,
                use_cache=False,
                return_dict=False,
            )[0]

    torch.set_num_threads(2)
    model = BartForConditionalGeneration.from_pretrained(
        source,
        local_files_only=True,
        use_safetensors=True,
        attn_implementation="eager",
    ).eval()
    model.config.use_cache = False
    output.mkdir(parents=True, exist_ok=True)
    destination = output / f"g2p-{accent}.onnx"
    with torch.no_grad():
        torch.onnx.export(
            Forward(model).eval(),
            (torch.tensor([[1, 5, 6, 7, 2]]), torch.tensor([[1, 5]])),
            str(destination),
            input_names=["input_ids", "decoder_input_ids"],
            output_names=["logits"],
            dynamic_axes={"input_ids": {1: "enc"}, "decoder_input_ids": {1: "dec"}, "logits": {1: "dec"}},
            opset_version=17,
            do_constant_folding=True,
        )
    onnx.checker.check_model(str(destination))
    config = model.config
    metadata = {
        "format": 1,
        "source": f"PeterReid/graphemes_to_phonemes_en_{accent}",
        "license": "Apache-2.0",
        "graphemes": config.grapheme_chars,
        "phonemes": config.phoneme_chars,
        "bos_id": config.bos_token_id,
        "eos_id": config.eos_token_id,
        "start_id": config.decoder_start_token_id,
        "unknown_id": 3,
        "max_steps": min(64, config.max_position_embeddings),
    }
    sidecar = output / f"g2p-{accent}.json"
    sidecar.write_text(json.dumps(metadata, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return {p.name: {"bytes": p.stat().st_size, "sha256": hashlib.sha256(p.read_bytes()).hexdigest()} for p in (destination, sidecar)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    report = {}
    for accent in ("us", "gb"):
        report.update(export(args.input / f"upstream-{accent}", args.output, accent))
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
