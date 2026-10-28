# Feature: web-agregador (código postal, tienda por cadena y la web)

**Estado:** en curso · **Creado:** 2026-10-05 · **Rama:** feat/supermercados-cli

La web que consume los CLIs: el usuario busca un producto, ve el precio en cada cadena y monta
su **cesta virtual** para decidir dónde comprar. Antes de buscar, declara **dónde está**.

## Specs

- **S11** — «lo ideal es que se introduzca el codigo postal y se seleccionen los supermercados
  de cada una de las cadenas mas cercano, a partir de ahi la busqueda». Flujo: código postal →
  tienda/almacén más cercano **por cadena** → catálogo y precios **de esa tienda**. La cadena
  que no permita fijar tienda debe decirlo, no fingir que la tiene.
- **S12** — «Usa las skills de better-ui y demas para que la web sea de verdad atractiva». El
  diseño pasa por `better-ui`, `better-interface`, `better-layout`, `better-typography`,
  `better-colors`, `better-accessibility` y `better-writing`, y por `gentle-design` para las
  direcciones visuales.
- **S13** — La web no inventa precios ni disponibilidad: si una cadena falla, se muestra su
  estado (bloqueada, sin cuota, sin cobertura) y la comparación sigue con las demás.

## Lo que ya existe (base de la web)

`multisuper find` (buscar en seis), `multisuper basket` (cesta virtual con totales en céntimos
y ranking), `multisuper basket candidates` (candidatos para que el usuario elija), y
`<cadena> cart` para el paso de entrega (hoy solo `dia`).

## Lo que hay que averiguar por cadena (S11)

| Cadena | Pista conocida | Incógnita |
|---|---|---|
| `dia` | La respuesta de búsqueda trae `cart.postal_code` (por defecto `28041`): la zona la fija el servidor | Cómo se envía el código postal (cabecera, cookie, endpoint de tienda) |
| `mas` | Magento expone `pickupLocations` y `setPickupStoreOnCart` | Cómo elegir la tienda antes de buscar y si cambia precios |
| `aldi` | Un índice Algolia por región (`pen`/`can`/`bal`) | Si hay granularidad por tienda o solo por región |
| `lidl` | Localizador de tiendas en la web | Si la API de búsqueda acepta tienda/almacén |
| `alcampo` | La compra online pide código postal antes de navegar | Endpoint de selección de tienda y si la sesión lo recuerda |
| `carrefour` | Bloqueado por Cloudflare; precios vía Superfudis | Si Superfudis acepta código postal como contexto |

## Tareas

- [x] **T29** Averiguar por cadena cómo se fija el código postal / la tienda y qué cambia en
      los precios; documentarlo con evidencia. Ruta: `gentle-ai-explore`. **Hecho:**
      - `dia` — la zona es **estado de servidor en la sesión** y se cambia con `PUT
        api/v1/common-aggregator/save-shipping-address?new_postal_code=<cp>` seguido de `POST
        …/reevaluate-current-cart`, que devuelve el diff `price_changes{sku_id, old_price,
        new_price}`. Verificación de cobertura: `GET …/check-service?postal_code=`; tienda
        más cercana: `POST …/location/search-term` (solo lectura). **Es la única cadena donde
        el precio cambia con la zona.** La identidad viaja como **cabecera `session_id`**.
      - `mas` — `pickupLocations` (anónimo) da el punto de recogida más cercano por código
        postal, pero el precio es de **ámbito de web**, no de tienda: elegir tienda no cambia
        el precio.
      - `aldi` — techo en región (`pen|can|bal`, el índice de Algolia). Sin dimensión de tienda.
      - `lidl` — localizador público (`live.api.schwarz/odj/stores-api/v2/…`, apikey embebida)
        para la tienda física; la búsqueda de catálogo no acepta tienda y no hay prueba de que
        el precio la siga.
      - `alcampo` — el código postal es una **puerta de la interfaz** (Ocado Smart Platform),
        no un argumento de la API: la búsqueda responde precios sin código postal.
      - `carrefour` — Superfudis no tiene campo de ubicación en su esquema. Nada.
      **Aviso para la web:** nuestro CLI no manda `session_id` en la búsqueda, así que DIA
      responde siempre con la zona por defecto **28041**. Nunca presentarla como la zona del
      usuario ni mezclarla con las demás filas.
- [ ] **T33** Declarar la **zona por cadena** y las capacidades en el contrato: por cadena,
      `zone: {mode: warehouse|pickup|region|none, price_scoped: bool}` y `cart: bool`.
      La web solo pide código postal a las cadenas que lo honran, y etiqueta cada fila con el
      alcance real de su precio (DIA zona de sesión, MAS catálogo nacional, ALDI región).
      Ruta: `gentle-ai-worker`.
- [ ] **T30** Direcciones visuales de la web (`gentle-design`, 3 artboards) con los datos
      reales del producto: buscar, comparar por cadena, montar cesta, ver totales y faltantes.
- [x] **T31** Implementar la web sobre los binarios. **Dirección elegida en el lienzo:
      `design/web-agregador/03-tienda-cercana.html`** (handoff del 2026-10-05, cerrado como
      implementado). Servidor `cmd/web` con `/api/caps`, `/api/search`, `/api/candidates`,
      `/api/basket`, `/api/cart/dia`, caché TTL de 60 s con `?fresh=1` para el reintento y
      errores por cadena en el sobre de los CLIs; `internal/capabilities` con la tabla real.
      Verificado: primera búsqueda 2,75 s y **0,0009 s** la segunda (caché), portada sin una
      sola URL externa, y recorrido completo con navegador sin cabeza: 6 tarjetas de tienda
      (3 sin selector), 24 resultados en 6 de 6 cadenas, 2 líneas de cesta, 5 selectores con 18
      opciones, y la recomendación anunciada por `aria-live`: «ALDI 2,85 €, 0,04 € menos que
      DIA». Sin errores de consola.
- [ ] **T34** Probar `/api/cart/dia` en vivo (hoy solo con stubs): llena una cesta anónima
      real de DIA, igual que `dia cart add`. Es el único paso del producto sin ejercitar.
- [ ] **T32** Identidad de **línea** en la cesta (bloquea la web): hoy una entrada fijada ata el
      artículo a **una** cadena, así que fijar huevos en DIA, ALDI y MAS da `1/3` de cobertura
      en todas y ningún ganador. Falta una clave de línea (`{"line":"huevos","store":"dia",
      "id":"305802","quantity":1}`) para que cada cadena tenga **su** producto de la misma
      línea. Sin esto, la web no puede dejar elegir producto por cadena, que es justo lo que
      el usuario pidió (L7).

## Log

- **Handoff de diseño (2026-10-05):** el lienzo elige `design/web-agregador/03-tienda-cercana.html`
  («Tienda cercana»: código postal y tiendas por cadena primero). Se implementa esa dirección
  sobre el backend común; `01-comparador-denso.html` y `02-decision-guiada.html` quedan como
  alternativas del mismo diseño. El traspaso no commitea por sí mismo: la implementación va en
  los commits de T31.
- **L8** (literal) «lo ideal es que se introduzca el codigo postal y se seleccionen los
  supermercados de cada una de las cadenas mas cercano, a partir de ahi la busqueda. Usa las
  skills de better-ui y demas para que la web sea de verdad atractiva»
- **Diseño:** `design/web-agregador/` con tres direcciones estructuralmente distintas sobre
  los datos reales de la cesta (leche + pan + huevos): `01-comparador-denso.html` (tabla
  densa con panel de cesta), `02-decision-guiada.html` (recomendación destacada y ranking,
  cesta editable que recalcula en céntimos), `03-tienda-cercana.html` (código postal y
  tiendas por cadena primero). Los cuatro estados de cadena existen en las tres: completa,
  falta artículo, sin cuota y sin respuesta. Ningún precio inventado: LIDL aparece «sin dato»
  con cobertura 2 de 3. Medido, no estimado: contraste de cada par declarado (el borde del
  botón secundario pasó de 1,82:1 a 3,40:1) y aritmética de la cesta verificada con asserts.
