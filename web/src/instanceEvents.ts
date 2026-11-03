import type { EchoEvent } from './types';

// Each source has its own sequence space. A reset starts a new recovery epoch;
// deltas arriving during the snapshot are replayed only past its watermark.
export class InstanceEvents {
  private sources = new Map<string, { sequence: number; pending: boolean; backlog: EchoEvent[] }>();
  private closed = false;
  constructor(private recover: (node: string, current: () => boolean) => Promise<number>, private deliver: (node: string, event: EchoEvent) => void, private failed: (error: unknown) => void) {}
  close() { this.closed = true; this.sources.clear(); }
  push(node: string, event: EchoEvent) {
    if (this.closed) return;
    if (event.type === 'reset') {
      const source = { sequence: 0, pending: true, backlog: [] as EchoEvent[] };
      this.sources.set(node, source);
      void this.recover(node, () => !this.closed && this.sources.get(node) === source).then(sequence => {
        if (this.closed || this.sources.get(node) !== source) return;
        source.sequence = sequence; source.pending = false;
        for (const queued of source.backlog) this.push(node, queued);
        source.backlog = [];
      }).catch(error => { if (!this.closed && this.sources.get(node) === source) this.failed(error); });
      return;
    }
    let source = this.sources.get(node);
    if (!source) { source = { sequence: 0, pending: false, backlog: [] }; this.sources.set(node, source); }
    if (source.pending) {
      if (source.backlog.length >= 2048) { this.failed(new Error('Event recovery exceeded its buffer; reconnecting')); return; }
      source.backlog.push(event); return;
    }
    if (event.sequence && event.sequence <= source.sequence) return;
    source.sequence = event.sequence || source.sequence;
    this.deliver(node, event);
  }
}
