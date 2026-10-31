import { useEffect, useRef, useState } from "react";
import { qrDataURL } from "./qr";

type QRCodeImageProps = {
  value: string;
  alt: string;
  size?: number;
  downloadName?: string;
  downloadLabel?: string;
  errorLabel: string;
};

export function QRCodeImage({
  value,
  alt,
  size = 220,
  downloadName,
  downloadLabel,
  errorLabel,
}: QRCodeImageProps) {
  const [source, setSource] = useState("");
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let active = true;
    setSource("");
    setFailed(false);
    if (!value.trim()) return;
    void qrDataURL(value, size * 2)
      .then((url) => {
        if (active) setSource(url);
      })
      .catch(() => {
        if (active) setFailed(true);
      });
    return () => {
      active = false;
    };
  }, [value, size]);

  if (failed) return <p className="form-error">{errorLabel}</p>;
  return (
    <div className="qr-code">
      {source ? (
        <img src={source} alt={alt} width={size} height={size} />
      ) : (
        <div className="qr-placeholder" style={{ width: size, height: size }} aria-label={alt} aria-busy="true" />
      )}
      {downloadName && source && (
        <a className="button ghost small" href={source} download={downloadName}>
          {downloadLabel ?? alt}
        </a>
      )}
    </div>
  );
}

type QRScanButtonProps = {
  label: string;
  busyLabel: string;
  scanning: boolean;
  disabled?: boolean;
  onFile: (file: File) => void;
};

export function QRScanButton({ label, busyLabel, scanning, disabled, onFile }: QRScanButtonProps) {
  const input = useRef<HTMLInputElement>(null);
  return (
    <>
      <button
        type="button"
        className="button ghost small qr-scan-button"
        onClick={() => input.current?.click()}
        disabled={disabled || scanning}
      >
        {scanning ? busyLabel : label}
      </button>
      <input
        ref={input}
        className="qr-file-input"
        type="file"
        accept="image/*"
        disabled={disabled || scanning}
        onChange={(event) => {
          const file = event.currentTarget.files?.[0];
          event.currentTarget.value = "";
          if (file) onFile(file);
        }}
      />
    </>
  );
}
