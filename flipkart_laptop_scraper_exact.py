"""Run the Flipkart laptop ETL.

Easy flow:
1. Find laptop links on search pages.
2. Read accurate specifications from each product page.
3. Validate the required 19 columns.
4. Write the Excel workbook.

Detailed implementation lives in ``flipkart_scraper/core.py`` so this entry
file stays short and readable.
"""

from flipkart_scraper.core import (
    COLUMNS,
    build_graphics,
    build_processor,
    clean_product_name,
    extract_specs,
    fetch,
    main,
    normalise_gpu,
    normalise_storage,
    parse_product_page,
    write_excel,
)


if __name__ == "__main__":
    raise SystemExit(main())
