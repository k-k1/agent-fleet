interface ActiveRecovery {
  id: number;
  epoch: number;
  key: string;
  promise: Promise<void>;
}

/**
 * Keeps recovery GETs single-flight and prevents superseded responses from
 * mutating editor state. Invalidating the epoch also covers file changes,
 * discard, and unmount while a request is in flight.
 */
export class RecoveryCoordinator {
  private sequence = 0;
  private epoch = 0;
  private active: ActiveRecovery | null = null;

  invalidate(): void {
    this.epoch++;
    this.active = null;
  }

  run<T>(key: string, request: () => Promise<T>, apply: (result: T) => void): Promise<void> {
    if (this.active?.key === key) return this.active.promise;

    const id = ++this.sequence;
    const epoch = this.epoch;
    const promise = request()
      .then((result) => {
        if (this.active?.id === id && this.active.epoch === epoch) apply(result);
      })
      .finally(() => {
        if (this.active?.id === id && this.active.epoch === epoch) this.active = null;
      });
    this.active = { id, epoch, key, promise };
    return promise;
  }
}
