package apiv2

func premiereFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "premiere_capabilities_unavailable", operationID: "getPremiereCapabilities", scenario: "Premiere settings stay hidden when the optional Live TV bridge is unavailable.", method: "GET", path: "/api/v2/premieres/capabilities", headers: profileOwner(), status: 200, schema: "#/components/schemas/PremiereCapability", assertHeaders: []string{"Cache-Control", "Content-Type", "ETag"}}}
}
