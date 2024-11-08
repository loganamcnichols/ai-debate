const BUFFER_SIZE = 1 << 15
const BUFFERING_CAP = 1 << 12;
const INPUT_SAMPLE_RATE = 24000;

const SPEECH_STARTED = 1;
const SPEECH_STOPPED = 0;
class InboundResampler extends AudioWorkletProcessor {
  constructor() {
    super();
    this.buffer = new Float32Array(BUFFER_SIZE);
    this.readIndex = 0;
    this.writeIndex = 0;
    this.bufferedFrames = 0;
    this.buffering = true;
    this.port.onmessage = (evt) => this.procMessage(evt);
    this.channelOpen = true;
    this.init();
  }

  async init() {
    const { create, ConverterType } = globalThis.LibSampleRate;

    let nChannels = 1;
    let inputSampleRate = 24000;
    let outputSampleRate = sampleRate;

    create(nChannels, inputSampleRate, outputSampleRate, {
      convertorType: ConverterType.SRC_SINC_BEST_QUALITY,
    }).then((src) => {
      this.src = src;
    });
  }



  procMessage(evt) {
    if (evt.data.length == 1) {
      if (evt.data[0] == SPEECH_STARTED) {
        this.channelOpen = false;
        this.buffer.fill(0);
      } else if (evt.data[0] == SPEECH_STOPPED) {
        this.channelOpen = true;
      }
      return;
    }

    if (!this.channelOpen) return;

    const pcm16 = evt.data;
    let dataLength = pcm16.length;
    const availableLength = BUFFER_SIZE - this.bufferedFrames;

    if (dataLength > availableLength) {
      console.log("buffer overflow");
      dataLength = availableLength;
    }

    for (let i = 0; i < dataLength; i++) {
      this.buffer[this.writeIndex++] = pcm16[i] / 32768.0;
      this.writeIndex %= this.buffer.length;
    }
    this.bufferedFrames += dataLength;
  }

  process(inputs, outputs) {
    const output = outputs[0];
    const channelCount = output.length;
    const sampleFrames = output[0].length;
    const requiredFrames = Math.floor(INPUT_SAMPLE_RATE / sampleRate * sampleFrames);

    if (this.bufferedFrames >= BUFFERING_CAP) {
      this.buffering = false;
    }

    if (this.bufferedFrames < requiredFrames) {
      this.buffering = true;
      return true;
    }

    if (this.buffering) {
      return true;
    }

    if (this.src == null) {
      console.log("src is null");
      return true;
    }

    const inputSample = new Float32Array(requiredFrames);
    const bufferSpace = this.buffer.length - this.readIndex;
    inputSample.set(this.buffer.slice(this.readIndex, this.readIndex + requiredFrames));
    this.readIndex += requiredFrames;
    if (requiredFrames > bufferSpace) {
      this.readIndex %= this.buffer.length;
      inputSample.set(this.buffer.slice(0, this.readIndex), bufferSpace);
    }

    this.bufferedFrames -= inputSample.length;

    const resampled = this.src.full(inputSample);

    if (resampled.length > sampleFrames) {
      throw new Error("resampled data larger than output");
    }

    for (let i = 0; i < resampled.length; i++) {
      // Output the sample to all channels
      for (let channel = 0; channel < channelCount; channel++) {
        output[channel][i] = resampled[i];
      }
    }

    const audioMS = resampled.length / sampleRate * 1000;
    this.port.postMessage(audioMS);
    return true;
  }
}

registerProcessor("inbound-resampler", InboundResampler);
