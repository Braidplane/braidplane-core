from html import escape
import os
import signal
import zipfile

from pyftpdlib.authorizers import DummyAuthorizer
from pyftpdlib.handlers import FTPHandler
from pyftpdlib.servers import FTPServer


def seed_spreadsheet_fixture(ftp_root: str) -> None:
    """Create a deterministic workbook fixture for spreadsheet connector tests."""
    target_path = os.path.join(ftp_root, "users.xlsx")
    os.makedirs(ftp_root, exist_ok=True)

    rows = {
        2: [("B", "email"), ("C", "id"), ("D", "total")],
        3: [("B", "alice@example.com"), ("C", 1), ("D", 42)],
        4: [("B", "bob@example.com"), ("C", 2), ("D", 19)],
    }

    with zipfile.ZipFile(target_path, "w", zipfile.ZIP_DEFLATED) as workbook:
        workbook.writestr("[Content_Types].xml", content_types_xml())
        workbook.writestr("_rels/.rels", root_rels_xml())
        workbook.writestr("xl/workbook.xml", workbook_xml())
        workbook.writestr("xl/_rels/workbook.xml.rels", workbook_rels_xml())
        workbook.writestr("xl/worksheets/sheet1.xml", worksheet_xml(rows))
        workbook.writestr("xl/styles.xml", styles_xml())


def content_types_xml() -> str:
    """Return the package content type manifest for the workbook fixture."""
    return """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
  <Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
  <Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>"""


def root_rels_xml() -> str:
    """Return package relationships for the workbook fixture."""
    return """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>"""


def workbook_xml() -> str:
    """Return workbook metadata with one named sheet."""
    return """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets>
    <sheet name="Orders" sheetId="1" r:id="rId1"/>
  </sheets>
</workbook>"""


def workbook_rels_xml() -> str:
    """Return workbook relationships for the worksheet and style parts."""
    return """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>"""


def worksheet_xml(rows: dict[int, list[tuple[str, object]]]) -> str:
    """Return worksheet XML with fixture cells placed at B2:D4."""
    rendered_rows = []
    for row_number, cells in rows.items():
        rendered_cells = "".join(cell_xml(column, row_number, value) for column, value in cells)
        rendered_rows.append(f'<row r="{row_number}">{rendered_cells}</row>')

    return f"""<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <dimension ref="B2:D4"/>
  <sheetData>
    {"".join(rendered_rows)}
  </sheetData>
</worksheet>"""


def cell_xml(column: str, row_number: int, value: object) -> str:
    """Return one worksheet cell using inline strings or numeric values."""
    cell_ref = f"{column}{row_number}"
    if isinstance(value, (int, float)):
        return f'<c r="{cell_ref}"><v>{value}</v></c>'

    text = escape(str(value), quote=False)
    return f'<c r="{cell_ref}" t="inlineStr"><is><t>{text}</t></is></c>'


def styles_xml() -> str:
    """Return a minimal workbook style sheet."""
    return """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts>
  <fills count="1"><fill><patternFill patternType="none"/></fill></fills>
  <borders count="1"><border/></borders>
  <cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
  <cellXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/></cellXfs>
</styleSheet>"""


def main() -> None:
    user = os.getenv("FTP_USER", "braidplane")
    password = os.getenv("FTP_PASS", "braidplanepass")
    ftp_root = os.getenv("FTP_ROOT", "/srv/ftp")
    public_host = os.getenv("FTP_PUBLIC_HOST", "127.0.0.1")
    passive_min = int(os.getenv("FTP_PASV_MIN", "32100"))
    passive_max = int(os.getenv("FTP_PASV_MAX", "32100"))

    seed_spreadsheet_fixture(ftp_root)

    authorizer = DummyAuthorizer()
    authorizer.add_user(user, password, ftp_root, perm="elrw")

    handler = FTPHandler
    handler.authorizer = authorizer
    handler.masquerade_address = public_host
    handler.passive_ports = range(passive_min, passive_max + 1)

    server = FTPServer(("0.0.0.0", 21), handler)

    def stop(_signum, _frame):
        raise SystemExit(0)

    signal.signal(signal.SIGTERM, stop)
    try:
        server.serve_forever(timeout=1, blocking=True)
    finally:
        server.close_all()


if __name__ == "__main__":
    main()
