package osrm

// Заготовка под пост-MVP интеграцию с OSRM.
//
// OSRM (Open Source Routing Machine) — внешний HTTP-сервис, развёрнутый
// в нашей инфраструктуре. Используем его эндпоинт /match для «map matching»:
// массив GPS-точек снэпается на сеть реальных дорог OpenStreetMap.
// Это естественным образом устраняет физически невозможные участки
// (пересечение зданий, телепортации) — то есть ту же проблему, что
// решает математический pipeline, но «по-настоящему».
//
// Формат запроса (черновик):
//
//	GET {OSRMURL}/match/v1/driving/{lon1},{lat1};{lon2},{lat2};...
//	    ?timestamps=t1;t2;...&overview=full&geometries=geojson&radiuses=...
//
// Ограничения OSRM: максимум ~100 точек на запрос — большие треки
// придётся резать на батчи и сшивать.

// // Client — HTTP-клиент к OSRM.
// type Client struct {
// 	BaseURL string
// }
//
// // New создаёт клиента.
// func New(baseURL string) *Client {
// 	return &Client{BaseURL: baseURL}
// }
//
// // Match снэпает массив точек на дорожную сеть.
// //
// // TODO (пост-MVP):
// //  1. Сформировать URL с координатами и timestamps.
// //  2. Разбить на батчи если len(points) > лимита OSRM.
// //  3. Распарсить ответ (matchings[].geometry.coordinates), маппить обратно в []Point.
// //  4. Учесть случаи code != "Ok" и tracepoints == null (точки, которые не удалось снэпнуть).
// func (c *Client) Match(ctx context.Context, points []geo.Point) ([]geo.Point, error) {
// 	return points, nil
// }
