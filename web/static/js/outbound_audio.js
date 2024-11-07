const TARGET_SAMPLE_RATE = 24000;
class OutboundResampler extends AudioWorkletProcessor {
  constructor() {
    super();
    this.init();
  }

  async init() {
    const { create, ConverterType } = globalThis.LibSampleRate;
    let nChannels = 1;
    console.log("sample rate", sampleRate);
    let inputSampleRate = sampleRate;
    let outputSampleRate = TARGET_SAMPLE_RATE;

    create(nChannels, inputSampleRate, outputSampleRate, {
      convertorType: ConverterType.SRC_SINC_BEST_QUALITY,
    }).then((src) => {
      this.src = src;
    });
  }

  floatTo16BitPCM(float32Array) {
    const buffer = new ArrayBuffer(float32Array.length * 2);
    const view = new DataView(buffer);
    let offset = 0;
    for (let i = 0; i < float32Array.length; i++, offset += 2) {
      let s = Math.max(-1, Math.min(1, float32Array[i]));
      view.setInt16(offset, s < 0 ? s * 0x8000 : s * 0x7fff, true);
    }
    return buffer;
  }

  process(inputs, outputs, params) {
    if (this.src == null) {
      throw new Error("Resampler not initialized");
    }

    // const resampled = this.src.full(inputs[0][0]);
    const pcm16Buffer = this.floatTo16BitPCM(inputs[0][0]);

    // Send the ArrayBuffer directly
    this.port.postMessage(pcm16Buffer, [pcm16Buffer]); // Transfer ownership
  }

}

registerProcessor("outbound-resampler", OutboundResampler);