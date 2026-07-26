/**
 * Marks asynchronous editor operations that may no longer own the model after
 * a discard, file change, or unmount.
 */
export class OperationEpoch {
  private epoch = 0;

  capture(): number {
    return this.epoch;
  }

  invalidate(): void {
    this.epoch++;
  }

  isCurrent(captured: number): boolean {
    return captured === this.epoch;
  }
}
