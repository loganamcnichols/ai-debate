const BUFFER_SIZE = 1 << 22;
const BUFFERING_CAP = 1 << 12;
const INPUT_SAMPLE_RATE = 24000;

const SPEECH_STARTED = 0;
const SPEECH_ENDED = 1;
const AUDIO_RECV = 2;

class InboundResampler extends AudioWorkletProcessor {
  constructor() {
    super();
    this.buffer = new Float32Array(BUFFER_SIZE);
    this.readIndex = 0;
    this.writeIndex = 0;
    this.bufferedFrames = 0;
    this.buffering = true;
    this.port.onmessage = (evt) => this.procMessage(evt);
  }

  procMessage(evt) {
    if (evt.data.length === 1
     && evt.data[0] === SPEECH_STARTED) {
      console.log("filling buffer with 0's");
      this.buffer.fill(0);
      return;
    }
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

  process(_, outputs) {
    const output = outputs[0];
    const channelCount = output.length;
    let sampleFrames = output[0].length;
    
    if (this.bufferedFrames >= BUFFERING_CAP) {
      this.buffering = false;
    }


    if (this.buffering) {
      console.log("buffering");
      return true;
    }

    if (this.bufferedFrames < sampleFrames) {
      this.buffering = true;
      sampleFrames = this.bufferedFrames;
    }

    const inputSample = new Float32Array(sampleFrames);
    const bufferSpace = this.buffer.length - this.readIndex;
    inputSample.set(this.buffer.slice(this.readIndex, this.readIndex + sampleFrames));
    this.readIndex += sampleFrames;
    if (sampleFrames > bufferSpace) {
      this.readIndex %= this.buffer.length;
      inputSample.set(this.buffer.slice(0, this.readIndex), bufferSpace);
    }

    this.bufferedFrames -= sampleFrames;

    for (const channel of output) {
      channel.set(inputSample);
    }

    const audioMS = sampleFrames / sampleRate * 1000;
    this.port.postMessage(audioMS);
    return true;
  }
}

registerProcessor("inbound-resampler", InboundResampler);
