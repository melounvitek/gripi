export class AsyncGeneration {
  constructor() {
    this.value = 0;
  }

  capture() {
    return this.value;
  }

  next() {
    this.value += 1;
    return this.value;
  }

  current(generation) {
    return generation === this.value;
  }
}
