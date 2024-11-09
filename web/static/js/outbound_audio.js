const TARGET_SAMPLE_RATE = 24000;
class OutboundResampler extends AudioWorkletProcessor {
  constructor() {
    super();
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

  int16ToFloat32(int16Array) {
      const float32Array = new Float32Array(int16Array.length);
      for (let i = 0; i < int16Array.length; i++) {
          float32Array[i] = int16Array[i] / 32768; // Normalize to range -1.0 to 1.0
      }
      return float32Array;
  }

  process(inputs, _) {
    const resampled = inputs[0][0];

    const pcm16Buffer = this.floatTo16BitPCM(resampled); 

    // Send the ArrayBuffer directly
    this.port.postMessage(pcm16Buffer, [pcm16Buffer.buffer]); // Transfer ownership

    return true;
  }

}

registerProcessor("outbound-resampler", OutboundResampler);