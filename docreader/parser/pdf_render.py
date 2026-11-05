"""Convert Office documents to PDF with LibreOffice for the in-app preview.

The PDF is a derived artifact: the app serves it for preview so the original
upload stays behind the knowledge-base download permission, and the browser
never has to render a 50 MB deck with a JavaScript layout engine.

Two details matter for citation navigation:

* ``ExportHiddenSlides`` keeps hidden slides in the PDF, so slide N maps to
  page N (the app's source locators are 1-based slide numbers).
* Each conversion gets its own LibreOffice profile directory, because a shared
  profile serialises conversions and one crashed run poisons the next.
"""

from __future__ import annotations

import io
import logging
import os
import shutil
import subprocess
import tempfile
import threading
from pathlib import Path


logger = logging.getLogger(__name__)

# A 56 MB deck converts in ~60 s and peaks around 1 GB RSS; anything beyond
# this is a document the operator should open locally instead.
MAX_RENDER_BYTES = 150 * 1024 * 1024
RENDER_TIMEOUT_SECONDS = 300

# LibreOffice picks the export filter from the document type. Presentations
# need impress_pdf_Export so the hidden-slide option is honoured.
_EXPORT_FILTERS = {
    "ppt": "impress_pdf_Export",
    "pptx": "impress_pdf_Export",
    "doc": "writer_pdf_Export",
    "docx": "writer_pdf_Export",
    "xls": "calc_pdf_Export",
    "xlsx": "calc_pdf_Export",
    "odp": "impress_pdf_Export",
    "odt": "writer_pdf_Export",
    "ods": "calc_pdf_Export",
}

_HIDDEN_SLIDES_OPTION = '{"ExportHiddenSlides":{"type":"boolean","value":"true"}}'

# Rasterising a PDF holds every page bitmap in memory until Pillow writes the
# preview PDF. Bound it separately so raising the Office render concurrency
# cannot exhaust the container on a large deck.
_PDF_RASTER_SLOTS = threading.Semaphore(2)


def _find_soffice() -> str | None:
    for candidate in ("soffice", "libreoffice"):
        path = shutil.which(candidate)
        if path:
            return path
    for path in ("/usr/bin/soffice", "/usr/local/bin/soffice"):
        if os.path.isfile(path):
            return path
    return None


def _rasterize_pdf(content: bytes) -> tuple[bytes, int]:
    """Turn a PDF into an image-only preview PDF.

    The preview surface must never hand back the original upload: a PDF is a
    source document, so its preview is a rasterised copy (no embedded fonts,
    no text layer, no source bytes). Page numbering is preserved, which keeps
    citation locators valid.
    """
    with _PDF_RASTER_SLOTS:
        try:
            import pypdfium2 as pdfium
            from PIL import Image
        except ImportError as exc:  # pragma: no cover - runtime venv always has both
            raise RuntimeError(f"PDF rasteriser is unavailable: {exc}") from exc
        document = pdfium.PdfDocument(io.BytesIO(content))
        page_count = len(document)
        if page_count == 0:
            raise RuntimeError("PDF contains no pages")
        pages = []
        for index in range(page_count):
            bitmap = document[index].render(scale=1.8).to_pil().convert("RGB")
            pages.append(bitmap)
        out = io.BytesIO()
        pages[0].save(
            out, format="PDF", save_all=True, append_images=pages[1:], resolution=130
        )
        return out.getvalue(), page_count


def _normalise_extension(file_name: str, file_type: str) -> str:
    ext = (file_type or "").strip().lstrip(".").lower()
    if not ext:
        ext = Path(file_name or "").suffix.lstrip(".").lower()
    return ext


def render_to_pdf(content: bytes, file_name: str, file_type: str) -> tuple[bytes, int]:
    """Convert one Office document to PDF and return (pdf_bytes, page_count).

    Raises ValueError for inputs this endpoint refuses (empty, too large,
    unsupported type) and RuntimeError when LibreOffice cannot produce a PDF.
    The caller reports the message to the app, which falls back to the original
    bytes or its client-side renderer.
    """
    if not content:
        raise ValueError("empty document")
    if len(content) > MAX_RENDER_BYTES:
        raise ValueError(f"document too large to render ({len(content)} bytes)")

    ext = _normalise_extension(file_name, file_type)
    if ext == "pdf":
        return _rasterize_pdf(content)
    export_filter = _EXPORT_FILTERS.get(ext)
    if export_filter is None:
        raise ValueError(f"unsupported file type for PDF render: {ext or 'unknown'}")

    soffice = _find_soffice()
    if not soffice:
        raise RuntimeError("LibreOffice is not available in the docreader image")

    target = f"pdf:{export_filter}"
    if export_filter == "impress_pdf_Export":
        target = f"{target}:{_HIDDEN_SLIDES_OPTION}"

    with tempfile.TemporaryDirectory(prefix="weknora-render-") as work_dir, \
            tempfile.TemporaryDirectory(prefix="weknora-lo-profile-") as profile_dir:
        source_path = os.path.join(work_dir, f"input.{ext}")
        Path(source_path).write_bytes(content)
        command = [
            soffice,
            "--headless",
            "--norestore",
            f"-env:UserInstallation={Path(profile_dir).as_uri()}",
            "--convert-to",
            target,
            "--outdir",
            work_dir,
            source_path,
        ]
        try:
            completed = subprocess.run(
                command, capture_output=True, timeout=RENDER_TIMEOUT_SECONDS, check=False
            )
        except subprocess.TimeoutExpired as exc:
            raise RuntimeError(f"PDF render timed out after {RENDER_TIMEOUT_SECONDS}s") from exc

        pdf_path = os.path.join(work_dir, "input.pdf")
        if completed.returncode != 0 or not os.path.isfile(pdf_path):
            stderr = (completed.stderr or b"").decode("utf-8", errors="ignore").strip()
            raise RuntimeError(
                f"LibreOffice could not render {ext} (exit {completed.returncode}): "
                f"{stderr[-400:] or 'no output'}"
            )

        data = Path(pdf_path).read_bytes()
        if not data:
            raise RuntimeError("LibreOffice produced an empty PDF")

    # Imported lazily so the module stays importable in environments without
    # the docreader virtualenv (the linter's interpreter, local tooling).
    import pypdf

    page_count = len(pypdf.PdfReader(io.BytesIO(data)).pages)
    logger.info(
        "Rendered %s (%d bytes) to PDF (%d bytes, %d pages)",
        ext, len(content), len(data), page_count,
    )
    return data, page_count