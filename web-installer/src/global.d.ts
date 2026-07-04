/// <reference types="vite/client" />

// Minimal Web Serial types (not in the default TS DOM lib).
interface SerialPort {
  open(options: { baudRate: number }): Promise<void>;
  close(): Promise<void>;
  setSignals(signals: { dataTerminalReady?: boolean; requestToSend?: boolean }): Promise<void>;
  readable: ReadableStream<Uint8Array> | null;
  writable: WritableStream<Uint8Array> | null;
}
interface Serial {
  requestPort(): Promise<SerialPort>;
}
interface Navigator {
  readonly serial: Serial;
}

// esp-web-tools custom element used directly in JSX.
declare namespace JSX {
  interface IntrinsicElements {
    "esp-web-install-button": React.DetailedHTMLProps<
      React.HTMLAttributes<HTMLElement> & { manifest?: string },
      HTMLElement
    >;
  }
}
