# Feature: supermercados-cli (dia-cli y mas-cli, fase catálogo)

**Estado:** en curso · **Creado:** 2026-10-05 · **Rama:** feat/supermercados-cli

Fase 1 de un agregador: dos binarios, uno por cadena, que buscan un producto y devuelven
**el mismo JSON**. Los carritos son fase 2 y no entran aquí.

## Specs

- **S1** — «un binario por cada supermercado.» Dos binarios independientes: `dia` y `mas`,
  cada uno con su propio paquete y su propio `cmd/`. No hay subcomando `--store`.
- **S2** — «mas es supermercadosmas.com.» El destino es `https://www.supermercadosmas.com`
  (Magento 2 + Hyvä, GraphQL en `/graphql`). No es masymas ni tienda.masymas.com.
- **S3** — «primero hacer los cli que se utilizaran por una aplicacion web que buscará un
  producto en todos los supermercados». Comando mínimo: `search <term>`. Con `--json`
  imprime el sobre `contract.Result` de `internal/contract` y **solo eso** en stdout;
  errores y logs a stderr. Códigos de salida, iguales en todos los binarios: `0` éxito,
  `1` error de obtención o de parseo, `2` error de uso (falta el término, `--limit` fuera de
  1..100, bandera desconocida). El término es la frase completa: `dia search leche entera`
  busca «leche entera», nunca solo «leche». Un `--limit` inválido se rechaza, no se recorta
  en silencio.
- **S4** — «luego se añadirán a carritos independientes (se evolucionará la idea mas
  adelante)». Fuera de alcance: sin login, sin carrito, sin checkout en esta feature.
- **S5** — «Si necesitas hacer REA, hazlo.» La fase de catálogo no lo requiere: las dos
  APIs están verificadas. REA queda reservado al carrito de DIA (fase 2).
- **S6** — «puedes analizar grocery-cli para reutilizar codigo.» Se reutiliza su contrato
  de datos (`internal/store/store.go`, MIT) como referencia de forma; el código se copia
  o se reescribe, nunca se importa (`internal/` no es importable entre módulos).
- **S7** — «en subagentes de aldi, lidl, carrefour y alcampo». Cuatro cadenas más con el
  mismo patrón que S1 y S3: un binario propio (`aldi`, `lidl`, `alcampo`, `carrefour`),
  un paquete propio y el mismo sobre `contract.Result`. Si una cadena no publica precios
  o no se deja leer sin navegador, se dice y no se simula.
- **S8** — Ningún CLI inventa un dato: sin precio publicado → `price` vacío y el hueco
  declarado en el README, nunca un precio estimado ni el de otra cadena.
- **S9** — Carrefour se lee **a través de Superfudis** (`POST
  https://supercomparador-i9tw.onrender.com/api/v1/compare-basket` con
  `retailers:["Carrefour"]`), no de `carrefour.es`, que está bloqueado por Cloudflare en
  todas sus rutas. Es una fuente declarada: el precio es *reportado por Superfudis*, no
  publicado por Carrefour, y así debe etiquetarse (S8). Límites reales: 10 productos por
  petición, 3 peticiones por minuto, sin endpoint de catálogo, y su ToS prohíbe la
  extracción masiva.

- **S10** — «realmente es una cesta de la compra virtual, para saber en funcion del precio
  del producto y la decisión del usuario donde comprar». La cesta **es nuestra**, no de la
  tienda: una lista de artículos que el usuario elige, con la que se calcula **una cesta
  candidata por supermercado** (mismos artículos, precio de cada cadena, qué falta) para que
  el usuario decida dónde comprar. Las cestas de tienda (`dia cart`, `mas cart`) dejan de ser
  el objetivo y pasan a ser el **paso de entrega** posterior, solo para la cadena elegida.

## Hechos verificados que condicionan la implementación

DIA (`GET https://www.dia.es/api/v1/search-back/search?q=<term>`): JSON anónimo, sin
bot-wall. Item: `object_id`, `display_name`, `brand`, `prices{price, price_per_unit,
measure_unit, strikethrough_price, is_promo_price}`, `units_in_stock`, `units_in_cart`,
`l1_category_description`, `l2_category_description`, `url`, `image`.

MAS (`POST https://www.supermercadosmas.com/graphql`): introspección abierta.
`price{regularPrice{amount{value currency}}}` funciona; **`finalPrice` no existe** y
**`price_range` devuelve HTTP 500** — no usarlos. `products(search:, pageSize:, currentPage:)`
y `total_count` verificados (245 resultados para «leche», 20 por página).

## Tareas

- [x] **T1** Andamiaje: `go.mod`, `internal/contract` (sobre JSON compartido). Inline.
- [ ] **T2** `mas-cli`: `search` contra `/graphql`, parseo de precio y €/unidad desde el
      nombre del producto. Ruta: `gentle-ai-worker` (A). Commit `feat(mas): …`
- [ ] **T3** `dia-cli`: `search` contra `search-back`, mapeo directo de `price_per_unit`
      y `measure_unit`. Ruta: `gentle-ai-worker` (B). Commit `feat(dia): …`
- [ ] **T4** Verificación independiente de ambos (`gentle-ai-verify`) + smoke real.
- [ ] **T5** `README.md` con el contrato JSON y los límites conocidos.
- [ ] **T6** `alcampo-cli`: `compraonline.alcampo.es/graphql` (existe: 405 a GET, exige POST).
      Ruta: `gentle-ai-worker`.
- [ ] **T7** `aldi-cli`: Next.js en `aldi.es` (buildId + rutas `/productos/...`), datos por
      `/_next/data/<buildId>/…json`. Ruta: `gentle-ai-worker`.
- [ ] **T8** `lidl-cli`: `lidl.es/q/api/search` responde 401; hay que descubrir sesión o
      cabeceras. Ruta: `gentle-ai-worker`.
- [ ] **T9** `carrefour-cli`: `carrefour.es` devuelve 403 Cloudflare. Requiere ingeniería
      inversa con navegador real o huella TLS. Ruta: `gentle-ai-explore` para el mapa y
      decisión del usuario antes de escribir código.
      **Mapa cerrado (explore):** `blocked-without-browser`. El 403 de Cloudflare
      (`__cf_bm`) cubre **todas** las rutas de catálogo y de API en ambos hosts `.es`
      (`/cloud-api/*`, `/graphql`, sitemaps, `/supermercado/...`) con lista blanca solo para
      `robots.txt` y `favicon.ico`; `carrefour.fr` y `carrefour.it` responden 200, así que el
      bloqueo es de zona, no del cliente. `api/compraonline/tienda/marketplace.carrefour.es`
      no resuelven (DNS). `robots.txt` sí permite las rutas de categoría y producto: lo que
      se rompe es Cloudflare, no el `robots`. Terceros: Superfudis (API pública, 3 req/min,
      ≤10 ítems, prohíbe extracción masiva, host en cold start), Open Prices (sin cobertura
      ES verificada), Apify/Bright Data (de pago, ~1,20 USD/1k productos). Sin superficie
      gratuita y legítima con precios por SKU.
- [ ] **T10** Desbloquear el smoke real de DIA (Akamai responde 200 de forma intermitente
      según huella TLS/orden de cabeceras). **Decidido:** añadir `utls` y presentar el
      ClientHello de Chrome, como hace `mercadona-cli`. Ruta: `gentle-ai-worker`.
- [ ] **T11** `carrefour-cli` sobre Superfudis (S9), etiquetando el precio como reportado.
      Ruta: `gentle-ai-worker`.
- [ ] **T12** Alinear `dia` y `mas` con S3: término de varias palabras, `--limit` validado
      en vez de recortado, y los tres códigos de salida.
- [ ] **T13** Presencia de precio en el contrato: hoy un producto sin precio se serializa
      `"price": 0` y se lee como 0,00 €. Diferido hasta que termine `alcampo` (toca
      `internal/contract`).
- [ ] **T14** Alinear `aldi` con S3: todavía recorta `--limit` en silencio con su propio
      `clampLimit` y `mas --help` sale con 1. Ruta: `gentle-ai-worker`. **Hecho.**
- [ ] **T15** Barrido final de `--help` en cualquier posición (`-h`/`--help`) para los seis
      binarios: hoy `aldi search --help`, `dia search --help` y `alcampo` (revisar) salen con
      2 y escriben la ayuda en stderr; `mas` ya sale con 0 y la escribe en stdout. Un solo
      sentido para toda la familia.
- [ ] **T16** REA sobre la **app Android de DIA**. **Hecho, con giro:** la app apunta al *mismo*
      `www.dia.es`, pero su búsqueda es `GET /api/v1/search-back/search/reduced` con cabeceras
      de dispositivo (`X-Id-Application-From: 8`, `X-Mob-Platform-From: OneApp-Android`,
      `order_source: app`, `x-avoid-seo-redirections`, `X-Mob-Os-Version`,
      `X-Mob-Application-Version`, `X-Mob-Manufacturer`, `X-Mob-Deviceid`,
      `X-Retail-Visitor-Id`, `X-Device-Trace-Id`, `X-App-Supports-L1-As-L2`) y `User-Agent:
      okhttp/4.12.0`. Sonda del padre: **5 de 5 HTTP 200**, 27 KB, con `search_items`,
      `pagination`, `total_items`, `sort` y `suggestions`. Con `curl` (TLS normal), así que la
      huella de uTLS **no era necesaria**. Documento: `odd/research/dia-android-api.md`.
- [ ] **T19** Mover `dia` al endpoint `reduced` con las cabeceras de la app y **retirar uTLS**
      (`go.mod` vuelve a stdlib); fixture real y smoke de cinco intentos. Ruta:
      `gentle-ai-worker`.
- [ ] **T21** Corregir los hallazgos de la verificación de la familia (T18):
      (1) `dia` emite `pricePerUnit` sin `unit` cuando `measure_unit` es `LAVADO` o `100 ML.`
      (25/30 hits de «detergente»): sin unidad conocida no hay precio unitario;
      (2) **`mas` inventa €/L** desde el nombre (`HANDY BAG 30l 12ud` → 0,09 €/L, `IFA SABE
      10ux100ml` → 13,5 L): se retira el parseo por nombre —MAS no publica tamaño de venta—
      y se documenta que MAS no da €/unidad;
      (3) `carrefour` afirma `available: true` sin dato de stock: `available` pasa a
      tri-estado (ausente = desconocido);
      (4) guard muerto de recorte en `internal/carrefour`.
- [ ] **T22** Corregir el README: `price_range` **no** devuelve 500 (lo que no existe es
      `final_price` dentro de él), el ejemplo JSON muestra campos que `mas` no rellena,
      «no hay paginación» contradice `--page`, «todas sus rutas» ignora el 200 de
      `robots.txt` y `/graphql` sin barra da 301. **Hecho.**
- [ ] **T23** Identificador común en el contrato: `ean` y `size` opcionales, rellenos solo
      donde la cadena los publica (carrefour ya los trae de Superfudis; `mas` hay que pedir
      el atributo en la selección GraphQL). Base de la fusión entre cadenas. Ruta:
      `gentle-ai-worker`.
- [ ] **T24** Agregador `multisuper find <term> --json`: seis cadenas en paralelo, timeout
      por cadena, resultado **parcial** con el estado de cada una, y errores
      máquina-legibles (`{"store":"dia","error":{"code":"blocked","status":403}}`).
      Ruta: `gentle-ai-worker` (paquetes nuevos, disjunto de T23).
- [ ] **T25** Cesta en `mas`: `createEmptyCart`, `addProductsToCart` sobre `itemsV2`,
      `cart get`, `cart clear`. **Parcial, por culpa de la tienda:** `add` es rechazado para
      todo SKU (`UNDEFINED: La cantidad solicitada no está disponible`; `only_x_left_in_stock`
      nulo, sin puntos de recogida). La tienda añade con un POST de formulario a
      `/checkout/cart/add`, no con GraphQL. `get`/`clear` funcionan en vivo.
- [ ] **T26** Cesta en `dia`: **hecho y verificado en vivo**. Formas leídas del APK
      (`ItemAddRequest{quantity, sku_id, position}`): `GET checkout-back/cart/reduced` (único
      payload con líneas), `POST|PUT cart/current/item`, `DELETE cart/current/item/{skuId}`,
      `DELETE cart/current/empty-cart`. Identidad = cookie `session_id` (DIA la emite en la
      primera llamada; `ak_bmsc` no hace falta), persistida `0600`. Errores de DIA
      (`206 VALIDATION_ERROR`, `unavailable`, `max_quantity_in_stock`) se reportan literales.
      Secuencia real: add 1 → 4,98 €; update 2 → 9,96 €; remove → 0,00 €; clear → `{cleared:true}`.

- [ ] **T27** Cesta **virtual** de comparación (S10): **hecho** (`ee51b27`). En vivo: DIA 4,17 €
      (3/3), MAS 4,38 € (3/3), ALDI 4,88 € (3/3), LIDL 2/3, ALCAMPO `quota`, CARREFOUR
      `timeout`. Descubrió el problema real: «huevos» se emparejó con *huevos de chocolate*
      (ALDI, 2,99 €) y con un *cuecehuevos* (LIDL, 11,99 €).
- [ ] **T28** Lista de candidatos y elección explícita (decisión del usuario):
      (1) modo `basket candidates --candidates N` que devuelve, por artículo y cadena, los N
      candidatos puntuados con id, nombre y precio;
      (2) la cesta acepta entradas fijadas `{"store","id","name","quantity"}` junto a las de
      `query`, y una entrada fijada se resuelve confirmando el **id** (si no aparece, la cadena
      responde `stale`, no `missing`).
      Así el emparejamiento deja de ser una apuesta silenciosa. Ruta: `gentle-ai-worker`.

## Log

- **L1** (literal) «cli de supermecardos. Creo que existe un mercadona-cli. la idea es
  implementar un dia-cli, un mas-cli en base a hacer rea (skill reverse engeering anything
  skill) al de mercadona.»
- **L2** (literal) «un binario por cada supermercado. mas es supermercadosmas.com. 3 la
  idea es primero hacer los cli que se utilizaran por una aplicacion web que buscará un
  producto en todos los supermercados y luego se añadirán a carritos independientes (se
  evolucionará la idea mas adelante). Si necesitas hacer REA, hazlo. 5. No exactamente,
  pero puedes analizar grocery-cli para reutilizar codigo.»
- **L3** (literal) «continua, lanza subagentes para trabajo en paralelo»
- **L4** (literal) «en subagentes de aldi, lidl, carrefour y alcampo»
- **L5** **Decisiones del usuario:** DIA → «Añadir utls y pelear el 200»; Carrefour →
  «Integrar Superfudis como fuente declarada». Registradas vía pregunta estructurada, no
  como texto literal.
- **Descubrimientos de los workers** (evidencia en sus informes): ALDI sirve su buscador
  con Algolia (`an_prd_es_es_<pen|can|bal>_products2`, credenciales search-only en el
  bundle); la ruta `/_next/data/<buildId>` está bloqueada con 308. Lidl devuelve 401 sin
  el parámetro `version`, cuyo valor **no se valida**; no hay login detrás. Superfudis
  responde 200 con `{"status":"ok","items":[{...,"offers":[{retailer,price_eur,
  match_type,...}]}]}`.
- **Decisión:** Go (stdlib, sin dependencias), mismo contrato de salida para las dos
  cadenas, `--json` como forma canónica y tabla legible como forma por defecto.
- **Decisión:** fuera de T2/T3 todo lo que no sea `search` (product, categories, batch).
  El agregador busca un producto; lo demás se añade cuando la web lo pida.
- **Deuda asumida:** MAS necesita derivar €/kg del nombre del producto (DIA lo da hecho).
  Es la pieza frágil: exige tests de tabla propios y no puede inventar unidades.
- **L7** **Decisión del usuario:** «El usuario elige de una lista de candidatos» — la máquina
  propone N candidatos puntuados por artículo y cadena, y la cesta admite la elección
  explícita por `id`. El emparejamiento automático sigue existiendo como propuesta, nunca
  como verdad.
- **L6** (literal) «realmente es una cesta de la compra virtual, para saber en funcion del
  precio del producto y la decisión del usuario donde comprar»
- **Corrección de rumbo por L6:** las cestas de tienda (`dia cart` en 21a291b, `mas cart` en
  77caf46) **no eran el objetivo**. Se conservan como paso de entrega, pero la pieza central
  es la cesta virtual (T27): el usuario elige artículos y el sistema le dice **cuánto le
  costaría cada supermercado y qué le faltaría**. El emparejamiento entre cadenas (nombre +
  `size`, ya que solo Carrefour tiene EAN) pasa a ser la pieza crítica, no un extra.
