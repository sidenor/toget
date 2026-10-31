import type { Translate } from "./i18n";

export type QRScanErrorCode = "image" | "tooLarge" | "notFound";

export class QRScanError extends Error {
  readonly code: QRScanErrorCode;

  constructor(code: QRScanErrorCode, message: string) {
    super(message);
    this.name = "QRScanError";
    this.code = code;
  }
}

const maxImageBytes = 20 * 1024 * 1024;
const maxImageDimension = 2000;

// decodeQRImage decodes one still image locally. The file never leaves the
// browser. Callers validate the payload prefix for the QR type they accept.
export async function decodeQRImage(file: File): Promise<string> {
  if (!file.type.startsWith("image/")) {
    throw new QRScanError("image", "Choose an image file.");
  }
  if (file.size > maxImageBytes) {
    throw new QRScanError("tooLarge", "That image is larger than 20 MB.");
  }
  let bitmap: ImageBitmap;
  try {
    bitmap = await createImageBitmap(file);
  } catch {
    throw new QRScanError("image", "Could not read that image.");
  }
  try {
    const scale = Math.min(1, maxImageDimension / Math.max(bitmap.width, bitmap.height));
    const width = Math.max(1, Math.round(bitmap.width * scale));
    const height = Math.max(1, Math.round(bitmap.height * scale));
    const canvas = document.createElement("canvas");
    canvas.width = width;
    canvas.height = height;
    const context = canvas.getContext("2d", { willReadFrequently: true });
    if (!context) throw new QRScanError("image", "Could not read that image.");
    // Transparent pixels decode as black; a white matte matches a printed code.
    context.fillStyle = "#fff";
    context.fillRect(0, 0, width, height);
    context.drawImage(bitmap, 0, 0, width, height);
    const pixels = context.getImageData(0, 0, width, height);
    // Keep the decoder out of the initial bundle; most sessions never scan.
    const { default: jsQR } = await import("jsqr");
    const found = jsQR(pixels.data, pixels.width, pixels.height, { inversionAttempts: "dontInvert" });
    const payload = found?.data.trim() ?? "";
    if (!payload) throw new QRScanError("notFound", "No QR code was found in that image.");
    return payload;
  } finally {
    bitmap.close();
  }
}

// qrDataURL renders a payload as a PNG data URL. The encoder is loaded on
// demand so the initial web bundle does not pay for it.
export async function qrDataURL(value: string, width = 440): Promise<string> {
  if (!value.trim()) throw new Error("QR payload is empty");
  const { default: QRCode } = await import("qrcode");
  return QRCode.toDataURL(value, {
    errorCorrectionLevel: "M",
    margin: 2,
    width,
    color: { dark: "#000000ff", light: "#ffffffff" },
  });
}

// qrScanErrorMessage maps decoder failures to caller-provided i18n keys:
// `${prefix}Image`, `${prefix}TooLarge`, and `${prefix}NotFound`.
export function qrScanErrorMessage(t: Translate, err: unknown, prefix: string): string {
  if (err instanceof QRScanError) {
    const suffix = err.code === "image" ? "Image" : err.code === "tooLarge" ? "TooLarge" : "NotFound";
    return t(`${prefix}${suffix}`);
  }
  return err instanceof Error ? err.message : t("common.error");
}
