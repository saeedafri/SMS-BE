# Flipkart Laptop Scraper Fix Guide

This document explains what was wrong in the original
`flipkart_laptop_scraper_exact.py`, why the Excel data was wrong, and how every
part of the replacement fixes the root cause.

It is written for someone who is learning Python and ETL.

## 1. What ETL means in this script

ETL means:

1. **Extract:** Download Flipkart search pages and product pages.
2. **Transform:** Convert Flipkart's raw HTML and JSON into clean laptop fields.
3. **Load:** Write those fields into an Excel workbook.

The repaired flow is:

```text
Flipkart search pages
        |
        | Find unique product IDs and URLs
        v
Flipkart product pages
        |
        | Read embedded structured JSON
        v
Clean and validate 19 laptop fields
        |
        | Remove only fully identical rows
        v
flipkart_laptops.xlsx
```

The main architectural change is important: search pages are now used only to
discover products. Product details come from each product's structured data.

## 2. Why the original output was wrong

The old script treated the complete visible text of a search result card as if
it were one reliable record:

```python
full_text = c.get_text(" ", strip=True)
```

That text mixed many unrelated values:

- the `Add to Compare` label;
- product title and specifications;
- current price and MRP;
- exchange values such as `Up to ₹42,600`;
- rating count and review count;
- offers, stock messages, and advertisements.

Regular expressions then searched that mixed string. A regular expression can
find matching text, but it cannot know whether `₹42,600` is an MRP, exchange
value, bank offer, or another product's value.

The old script also assigned values that it had never extracted:

```python
"graphics": "Not Available",
"pixels": "Not Available",
"seller": "Not Available",
"availability": "In Stock",
"touchscreen": "Not Available",
```

`availability = "In Stock"` was especially dangerous because it converted an
unknown value into a false fact.

Finally, the file contained repeated scraper and cleanup programs in the same
module. Importing the module started the scrape immediately. The result depended
on which duplicated block had last overwritten a column.

## 3. Issue-by-issue root cause and fix

### Issue 1: Product name contained the complete product information

**Old behavior**

```text
Add to CompareHP Victus AMD Ryzen 7 - (24 GB/1 TB SSD/Windows 11/...)
```

**Root cause**

The old code used all text inside the product link. That link also contained the
compare control and specification text. A later cleanup split only on ` - (`, so
it still left `Add to Compare`, processors, promotions, and bundle descriptions.

**Fix**

Lines 140-153 implement `clean_product_name()`.

The function now:

1. prefers Flipkart's structured `Brand` and `Series` fields;
2. removes processor-like parenthetical text;
3. removes bundle text beginning with words such as `with`;
4. removes marketing suffixes such as Office, AI PC, Backlit, and Metal Body;
5. removes `Add to Compare` when structured series data is missing;
6. removes the ` - (` specification block as a fallback;
7. removes a processor suffix if it is still present.

Example:

```text
Before: ASUS Vivobook S14 (2025) with Office 2024 + M365 Basic*, AI PC, Backlit Keyboard
After:  ASUS Vivobook S14 (2025)
```

Regression test: `test_product_name_removes_bundle_and_marketing_spec_text`.

### Issue 2: Graphics, pixels, and seller were unavailable

**Root cause**

The old code never tried to extract these values. It wrote `Not Available`
directly into every row.

**Graphics fix**

Lines 233-249 read dedicated graphics memory, memory type, and GPU family.
Lines 252-309 clean the GPU model. The output now distinguishes examples such
as:

```text
Graphics: Dedicated 4 GB GDDR6
GPU:      NVIDIA GeForce RTX 3050
```

or:

```text
Graphics: Integrated
GPU:      Intel UHD Graphics
```

**Pixels and seller scope**

The final requested schema contained exactly 19 columns and did not include
`Pixels` or `Seller`. They were therefore intentionally excluded from the final
workbook. Flipkart's structured data does expose `Screen Resolution`, and the
visible product page exposes seller information, so these can be added later if
the required schema changes to 21 columns.

This was a deliberate schema decision, not an extraction failure.

Regression tests:

- `test_graphics_distinguishes_integrated_and_dedicated_gpu`;
- `test_gpu_names_are_canonical_not_repeated_source_fragments`;
- `test_gpu_rejects_wrong_vendor_for_integrated_snapdragon_graphics`.

### Issue 3: Some MRP, processor, RAM, storage, ratings, and review values were missing

**Root cause**

The original regular expressions were too narrow and searched mixed card text.
Examples:

- MRP could become the exchange value `₹42,600`.
- The processor expression did not cover Core Ultra, Snapdragon, Kompanio, or
  many processor variants.
- RAM parsing depended on the word `RAM` appearing in one exact format.
- Storage supported only SSD and HDD in one exact title format.
- Rating parsing expected text next to `Rating`, but Flipkart renders rating and
  review elements separately.
- `review_count` extracted `Ratings`, not the actual `Reviews` count.

**Fix**

The repaired parser reads several independent structured sources:

| Field | Primary source | Fallback |
|---|---|---|
| Price | `ppd.finalPrice` | `ppd.fsp`, then schema offer price |
| MRP | `ppd.mrp` | `Not Available` rather than a guessed value |
| Rating | schema `aggregateRating.ratingValue` | visible rating text |
| Review counts | schema `aggregateRating.reviewCount` | visible `Reviews` text |
| Processor | Processor Brand + Name + Variant | title patterns |
| RAM | RAM + RAM Type | title pattern |
| Storage | capacity specification fields | title pattern |

Relevant code:

- lines 56-63 convert formatted numeric text into real numbers;
- lines 132-137 retrieve Flipkart's price object;
- lines 156-179 assemble processor data without repeated brands;
- lines 182-188 assemble RAM capacity and type;
- lines 200-230 normalize storage;
- lines 359-374 select price, MRP, rating, and review count;
- lines 404-411 provide rating/review fallbacks.

The parser does not invent ratings or reviews for products that have never been
rated. In the verified workbook, those genuinely unpublished source values stay
`Not Available`.

### Issue 4: Some rows were messy

**Root causes**

There were four separate causes:

1. all card text was merged into one string;
2. processor brands could repeat, such as `MediaTek MediaTek Kompanio`;
3. Flipkart sometimes repeats GPU fragments, such as
   `NVIDIA GeForce RTX RTX 3050`;
4. repeated searches returned the same product more than once.

**Fixes**

- Lines 42-43 collapse repeated whitespace.
- Lines 46-53 treat `NA`, `N/A`, `null`, and similar markers as missing values.
- Lines 78-109 isolate the product specification widget instead of accepting
  recommendation-widget specifications.
- Lines 156-166 prevent processor-brand repetition.
- Lines 252-309 convert noisy GPU strings into canonical names.
- Lines 435-440 remove tracking parameters while retaining the product ID.
- Lines 488-505 deduplicate discovered product IDs.
- Line 547 removes rows that are identical across all 19 output fields.

Examples:

```text
MediaTek MediaTek Kompanio 520  -> MediaTek Kompanio 520
NVIDIA GeForce RTX RTX 3050     -> NVIDIA GeForce RTX 3050
AMD Radeon Radeon 610M Graphics -> AMD Radeon 610M
```

### Issue 5: Headers appeared as the first data row

**Root cause**

The previous workflow produced CSV files and then passed them through multiple
cleanup stages. Spreadsheet import settings can treat the CSV header as normal
data. The script did not produce or validate an Excel workbook.

**Fix**

Lines 543-575 create a real `.xlsx` file with:

```python
frame.to_excel(writer, sheet_name="Laptops", index=False, header=True)
```

- `header=True` writes the 19 keys as Excel column headers.
- `index=False` prevents an unwanted `Unnamed: 0` column.
- `freeze_panes = "A2"` keeps the header visible while scrolling.
- `auto_filter` enables filtering from the header row.
- bold white text and a blue fill visually separate headers from data.

Regression test: `test_excel_has_real_headers_and_no_dataframe_index_column`.

### Issue 6: Unavailable and coming-soon products appeared in stock

**Root cause**

The original script contained this constant assignment for every product:

```python
"availability": "In Stock"
```

It never inspected Flipkart's availability value.

**Fix**

Lines 312-325 map both visible status messages and schema.org availability
values:

| Flipkart/source value | Excel value |
|---|---|
| `InStock` | `In Stock` |
| `OutOfStock` | `Currently Unavailable` |
| `PreOrder` or `PreSale` | `Coming Soon` |
| `BackOrder` | `Back Order` |
| `Discontinued` | `Discontinued` |
| Visible `Sold Out` | `Out of Stock` |

Unknown availability becomes `Not Available`, never `In Stock`.

Regression test:
`test_unavailable_and_coming_soon_are_never_marked_in_stock`.

### Issue 7: Brand and laptop type were missing or inaccurate

**Root cause**

The old brand logic used the first word of the polluted product name:

```python
brand = name.split()[0]
```

That returned `Add` when a name began with `Add to Compare`. Laptop type was a
small keyword guess that defaulted almost everything to `Personal`.

**Fix**

- Lines 351-354 read Brand from structured schema/specification data.
- Line 368 uses a safe fallback only when structured Brand is absent.
- Line 369 uses Flipkart's structured `Type` value.
- Lines 328-338 classify Gaming, 2 in 1, Thin and Light, and Chromebook only as
  fallback behavior.

This produces values such as `Gaming Laptop`, `Thin and Light Laptop`,
`Business Laptop`, `Chromebook`, and `Notebook` from the source.

### Issue 8: Storage did not say SSD or HDD

**Root cause**

The old `storage` column combined capacity and type in a loosely parsed string.
It did not create a dependable storage-type column, and it ignored eMMC and UFS.

**Fix**

Lines 200-230 return two separate values:

```text
Storage:      512 GB
Storage type: SSD
```

Supported combinations include:

- SSD;
- HDD;
- SSD + HDD;
- eMMC;
- SSD + eMMC;
- UFS.

The field name is `HDD`, not `HSD`.

Regression tests:

- `test_storage_combines_hdd_and_ssd_in_one_consistent_gb_value`;
- `test_storage_supports_ufs_and_flipkart_emmc_capacity_labels`.

### Issue 9: TB and GB values were inconsistent

**Root cause**

The original script copied the displayed unit. This made sorting and comparison
hard because `1 TB` and `512 GB` used different units.

**Fix**

Lines 191-197 convert every storage capacity into GB:

```text
1 TB         -> 1024 GB
512 GB       -> 512 GB
1 TB + 256 GB -> 1280 GB
```

Lines 227-230 add multiple installed storage devices and emit one total GB value.

### Issue 10: GPU was missing

**Root cause**

The old schema had no GPU output logic and set the broader graphics field to
`Not Available`.

**Fix**

Lines 252-309 recognize and clean:

- NVIDIA GeForce RTX, GTX, and MX;
- AMD Radeon and Radeon RX;
- Intel Arc, Iris Xe, UHD, and HD;
- Qualcomm Adreno;
- ARM Mali;
- Apple integrated GPU.

The code also checks processor/GPU compatibility. For example, a Snapdragon
laptop cannot truthfully use a stray AMD GPU label copied from another widget.
It becomes `Qualcomm Adreno Integrated Graphics`.

`Graphics` answers whether graphics are integrated or dedicated and can include
memory capacity/type. `GPU` answers which graphics processor model/family is
installed.

## 4. Complete source-code walkthrough

This section covers every non-blank line in the repaired 617-line script. Small
related lines are grouped so the explanation stays readable.

| Current lines | What the lines do | Why they exist |
|---:|---|---|
| 1 | Module description | Explains the script's single responsibility. |
| 3 | Enables modern type annotations | Allows annotations such as `int | float` safely. |
| 5-13 | Python standard-library imports | CLI arguments, JSON parsing, regex, threads, delays, paths, types, and safe URL handling. |
| 15-18 | Third-party imports | Pandas writes tabular data, Requests downloads pages, BeautifulSoup parses HTML, and openpyxl formats Excel. |
| 21-23 | Base URLs and missing-value constant | Keeps repeated values consistent and editable in one place. |
| 24-29 | Exact output-column contract | Prevents accidental columns, wrong order, and header drift. |
| 30-38 | Browser-like HTTP headers | Reduces responses intended for bots or unsupported clients. |
| 39 | Thread-local storage | Gives each parallel worker its own Requests session. |
| 42-43 | `clean_text` | Removes newlines and repeated whitespace from every source string. |
| 46-53 | `first_available` | Selects the first real value and rejects fake missing markers. |
| 56-63 | `number` | Converts `₹84,990`, `2,479`, and `4.4` into numeric values. |
| 66-75 | `_text_value` | Reads Flipkart's nested `{value: {text: ...}}` objects. |
| 78-86 | Recognized specification names | Defines which labels identify a real laptop-specification widget. |
| 88-104 | Recursive specification collector | Walks nested dictionaries/lists and creates label-to-value pairs. |
| 106-109 | Main-widget selection | Scores widgets and chooses the one containing the most laptop fields, avoiding recommendation contamination. |
| 112-118 | `extract_initial_state` | Finds and parses `window.__INITIAL_STATE__`, Flipkart's embedded JSON data. |
| 121-129 | `find_product_schema` | Finds the schema.org object whose type is Product. |
| 132-137 | `get_price_data` | Retrieves Flipkart's price/MRP object through safe dictionary access. |
| 140-148 | Structured product-name cleaning | Builds a concise Brand + Series name and removes bundle/marketing suffixes. |
| 149-153 | Product-name fallback | Cleans the visible title if Series is unavailable. |
| 156-166 | Structured processor assembly | Joins brand, family, and variant without repeating words. |
| 167-179 | Processor fallback patterns | Recognizes common Intel, AMD, Apple, MediaTek, and Qualcomm names from titles. |
| 182-188 | RAM construction | Joins RAM capacity and technology, with a title fallback. |
| 191-197 | Capacity conversion | Converts TB/GB text into one numeric GB value. |
| 200-207 | Supported capacity fields | Maps SSD, HDD, eMMC, and UFS labels to canonical types. |
| 208-212 | Structured storage extraction | Collects each installed device's capacity and type. |
| 213-219 | Storage title fallback | Extracts storage from product title only when structured capacity is absent. |
| 220-224 | Declared storage-type merge | Preserves a source type even when its capacity field uses another label. |
| 225-230 | Final storage output | Returns honest missing data or a summed GB capacity and ordered type string. |
| 233-241 | Dedicated-graphics detection | Uses memory capacity and known discrete GPU families. |
| 242-249 | Integrated-graphics classification | Returns Integrated, Dedicated, or Not Available without guessing blindly. |
| 252-258 | GPU input cleanup | Removes source trademarks/noise and identifies dedicated-graphics evidence. |
| 259-263 | Snapdragon/MediaTek checks | Prevents cross-vendor GPU contamination and supplies correct integrated families. |
| 264-271 | NVIDIA normalization | Converts repeated RTX/GTX/MX fragments into one canonical model. |
| 272-281 | AMD normalization | Handles Radeon RX, integrated Radeon, and numbered Radeon models. |
| 282-297 | Intel normalization | Handles Arc, Iris Xe, UHD, HD, and generic integrated graphics. |
| 298-309 | Mali, Apple, and missing fallbacks | Produces clean integrated names without fabricating an exact unpublished model. |
| 312-325 | Availability mapping | Replaces the old hard-coded `In Stock` with observed source status. |
| 328-338 | Laptop-type fallback | Classifies common families only when structured Type is absent. |
| 341-350 | Product-page parser setup | Extracts state, schema, specs, price data, visible text, and title. |
| 351-358 | Brand/processor/storage/GPU preparation | Builds reusable clean values before constructing the row. |
| 359-365 | Price, MRP, discount, and rating setup | Uses structured values and calculates discount from price and MRP. |
| 366-386 | The 19 output fields | Creates one complete record in the exact requested schema. |
| 387 | Column-order enforcement | Rebuilds the dictionary in `COLUMNS` order. |
| 390-401 | GPU title fallback | Extracts common GPU names if structured GPU is absent. |
| 404-411 | Rating/review text fallbacks | Reads visible values only when structured aggregate data is absent. |
| 414-416 | Display fallback | Finds a `cm (inch)` display measurement. |
| 419-420 | Touchscreen fallback | Recognizes Touch, 2-in-1, and Convertible titles. |
| 423-425 | Operating-system fallback | Recognizes Windows, Chrome OS, macOS, DOS, and Ubuntu. |
| 428-432 | Laptop-use fallback | Maps a known laptop type to a sensible broad use category. |
| 435-440 | Canonical URL builder | Removes tracking noise but retains the product ID needed to identify variants. |
| 443-461 | Search-card parser | Extracts only product ID, URL, title fallback, visible status, and card fallback text. |
| 464-469 | Per-thread HTTP session | Reuses connections safely without sharing one Session across threads. |
| 472-485 | Network fetch and retry | Applies timeouts, status checks, short-response detection, retries, and clear errors. |
| 488-505 | Product discovery | Scans pages, stops at the real end, and deduplicates product IDs. |
| 508-516 | Search-card fallback row | Preserves partial data if a detail page fails after all retries. |
| 519-527 | One-product worker | Downloads and parses one detail page or returns a labeled fallback. |
| 528-540 | Parallel detail processing | Processes multiple products concurrently while retaining original order and reporting fallbacks. |
| 543-547 | DataFrame creation and exact deduplication | Creates the 19-column table and removes only fully identical rows. |
| 548-557 | Excel header creation | Writes real headers, freezes row 1, enables filters, and styles headers. |
| 558-567 | Excel widths and wrapping | Keeps long values readable without manually resizing every column. |
| 568-574 | Excel numeric formats | Displays Price/MRP as rupees and Rating with one decimal place. |
| 575 | Returns the output path | Allows the caller and tests to inspect the created file. |
| 578-590 | Row validation | Refuses empty output, wrong columns, and unknown availability values. |
| 592-598 | CLI options | Defines page count, query, worker count, delay, and output filename. |
| 599-602 | CLI input validation | Rejects zero or negative pages/workers before network work begins. |
| 605-613 | `main` ETL orchestration | Runs Extract, Transform, Validate, and Load in order. |
| 616-617 | Import-safe entry point | Runs the scraper only when executed directly, not when imported by tests/tools. |

## 5. How one product becomes one Excel row

For each product, `parse_product_page()` performs these steps:

1. Parse the HTML.
2. Read the embedded JSON state.
3. Find the main Product schema.
4. Find the main laptop specification widget.
5. Read structured price and MRP.
6. Clean the product name.
7. Build processor and RAM descriptions.
8. convert and combine storage capacity into GB.
9. classify graphics and normalize the GPU.
10. map availability.
11. place every value into the exact 19-column dictionary.
12. rebuild the dictionary in the required column order.

The final field mapping is:

| Excel column | Transformation |
|---|---|
| Product name | Clean Brand + Series |
| Brand | Specification, then Product schema |
| Laptop type | Specification Type, then safe title fallback |
| Price | Final/selling price as a number |
| Discount | `(MRP - Price) / MRP × 100` |
| MRP | Structured MRP as a number |
| Rating | Average star rating |
| Review counts | Number of written reviews, not rating count |
| Processor | Brand + family + variant |
| RAM | Capacity + DDR/LPDDR type |
| Storage | Total installed capacity in GB |
| Storage type | SSD/HDD/eMMC/UFS combination |
| Graphics | Integrated or Dedicated, plus memory where published |
| Display size | Centimeters and inches from Flipkart |
| GPU | Canonical graphics processor family/model |
| Availability | Actual source status |
| Touchscreen or not | Yes/No from specification |
| Operating system | Structured OS |
| Laptop use | `Suitable For` specification or fallback category |

## 6. Why the script reads product pages

Reading only search cards is faster, but it cannot reliably provide seller,
touchscreen, GPU, exact storage type, laptop type, or use category. It also
mixes prices and offers in one block of text.

The repaired script pays the cost of one product-page request per unique product
because correctness matters more than a fast but wrong spreadsheet. Six workers
keep that approach practical without creating one shared, unsafe HTTP session.

## 7. Test file walkthrough

The tests are in `test_flipkart_laptop_scraper_exact.py`.

| Test lines | What is protected |
|---:|---|
| 1-9 | Imports and path to the real scraper module. |
| 12-22 | Importing the module must not start a scrape. |
| 25-30 | Loads the real scraper once for parser tests. |
| 32-96 | Proves one structured product becomes the exact expected 19-field row. |
| 98-105 | Proves 1 TB + 256 GB becomes 1280 GB and SSD + HDD. |
| 107-115 | Proves UFS and alternate eMMC labels are supported. |
| 117-123 | Prevents repeated processor brands. |
| 125-128 | Distinguishes integrated and dedicated graphics. |
| 130-142 | Prevents recommendation widgets from overwriting product specifications. |
| 144-146 | Prevents a false AMD GPU on a Snapdragon product. |
| 148-157 | Removes repeated NVIDIA, AMD, Intel, and Qualcomm GPU fragments. |
| 159-162 | Removes bundle/marketing text from product names. |
| 164-177 | Prevents unavailable and coming-soon products from becoming in stock. |
| 179-193 | Proves the real Excel first row contains exactly 19 headers and no index. |
| 195-202 | Removes rows identical across all requested fields. |
| 204-213 | Builds realistic nested Flipkart test data. |
| 216-217 | Allows direct test execution. |

## 8. How to run the scraper

From the `SMS-BE` directory:

```bash
cd /Users/mohdsaeedafri/All-Code-Base/SMS-BE
python3 flipkart_laptop_scraper_exact.py
```

Default behavior:

- query: `laptop`;
- maximum search pages: 50;
- product-detail workers: 6;
- delay between search pages: 0.3 seconds;
- output: `flipkart_laptops.xlsx`.

For a small test run:

```bash
python3 flipkart_laptop_scraper_exact.py \
  --pages 1 \
  --workers 2 \
  --output flipkart_laptops_smoke.xlsx
```

For another search:

```bash
python3 flipkart_laptop_scraper_exact.py \
  --query "gaming laptop" \
  --pages 10 \
  --output gaming_laptops.xlsx
```

## 9. How to run the tests

```bash
cd /Users/mohdsaeedafri/All-Code-Base/SMS-BE
python3 -m unittest -v test_flipkart_laptop_scraper_exact.py
```

Expected result:

```text
Ran 13 tests
OK
```

## 10. Final verified result

The final live run produced:

- 872 unique product pages processed;
- 0 network/parser fallbacks;
- 862 Excel rows after removing 10 fully identical records;
- 19 columns in the exact requested order;
- 0 polluted `Add to Compare` names;
- 0 exact duplicate output rows;
- 0 invalid storage units;
- 0 repeated GPU-name fragments;
- 13 passing regression tests.

Availability in that workbook:

| Availability | Rows |
|---|---:|
| In Stock | 769 |
| Out of Stock | 72 |
| Currently Unavailable | 18 |
| Coming Soon | 3 |

Storage types in that workbook:

| Storage type | Rows |
|---|---:|
| SSD | 812 |
| eMMC | 30 |
| SSD + HDD | 7 |
| HDD | 6 |
| UFS | 4 |
| SSD + eMMC | 3 |

One Acer product publishes only a maximum configurable storage value, not the
capacity installed in the sold unit. Its Storage value remains `Not Available`
to avoid turning a marketing maximum into false product data.

Some new/unrated products do not have a published average rating or written
reviews. Those values also stay `Not Available`. Missing source data and failed
extraction are not the same thing.

## 11. Troubleshooting

### `ModuleNotFoundError`

Install the required packages:

```bash
python3 -m pip install requests beautifulsoup4 pandas openpyxl
```

`cloudscraper` is no longer required.

### A search page fails

The script retries each request three times. If one search page still fails, it
prints the failure and continues with the next page.

### A product detail page fails

The script reports `Detail fallback for product ...` and creates a partial row
from its search card. The fallback count is printed every ten products. A clean
full run should show `0 fallbacks`.

### Flipkart changes its page structure

Run the 13 tests first. If tests pass but live output suddenly has many
fallbacks, inspect these boundaries in order:

1. `parse_search_page()` for product discovery;
2. `extract_initial_state()` for embedded JSON;
3. `extract_specs()` for specification labels;
4. `find_product_schema()` and `get_price_data()` for schema paths.

Do not start by adding more regular expressions to the full page text. Find the
new structured source first.

## 12. Rules for future changes

1. Keep `COLUMNS` as the only output-schema definition.
2. Add a failing regression test before changing parser behavior.
3. Prefer structured JSON over visible page text.
4. Never default unknown availability to `In Stock`.
5. Never invent MRP, rating, reviews, storage, or exact GPU models.
6. Keep numeric Price, MRP, Rating, and Review counts as numbers when available.
7. Normalize storage to GB before writing Excel.
8. Distinguish Graphics classification from GPU model.
9. Run the full test suite after every change.
10. Inspect the generated workbook, not only the terminal success message.
