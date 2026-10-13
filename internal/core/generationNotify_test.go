package core_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib/connectors"
	connectorsmocks "github.com/a-novel/service-genai/internal/lib/connectors/mocks"
)

func TestGenerationNotify(t *testing.T) {
	t.Parallel()

	const testNotifier = "notifier"

	errCheck := errors.New("check failed")

	type notificationMock struct {
		resp *connectors.Notification
		err  error
	}

	type daoMock struct {
		resp *dao.Generation
		err  error
	}

	testCases := []struct {
		name string

		request *core.GenerationNotifyRequest

		notificationMock *notificationMock
		daoMock          *daoMock
		// checkMock is the check's error; nil expects no check.
		checkMock *error

		expect    *core.GenerationNotifyResult
		expectErr error
	}{
		{
			name: "Success",

			request:          &core.GenerationNotifyRequest{Provider: testNotifier, Body: []byte("{}")},
			notificationMock: &notificationMock{resp: &connectors.Notification{CallID: testCallID}},
			daoMock:          &daoMock{resp: runningGeneration(1)},
			checkMock:        new(error),

			expect: &core.GenerationNotifyResult{},
		},
		{
			// An event the service does not act on is acknowledged, so the provider stops sending it.
			name: "Success/Ignored",

			request:          &core.GenerationNotifyRequest{Provider: testNotifier, Body: []byte("{}")},
			notificationMock: &notificationMock{err: connectors.ErrNotificationIgnored},

			expect: &core.GenerationNotifyResult{},
		},
		{
			// Purged since, or started by another user of the provider account.
			name: "Success/UnknownCall",

			request:          &core.GenerationNotifyRequest{Provider: testNotifier, Body: []byte("{}")},
			notificationMock: &notificationMock{resp: &connectors.Notification{CallID: testCallID}},
			daoMock:          &daoMock{err: dao.ErrGenerationGetByCallNotFound},

			expect: &core.GenerationNotifyResult{},
		},
		{
			name: "Error/Invalid",

			request:          &core.GenerationNotifyRequest{Provider: testNotifier, Body: []byte("{}")},
			notificationMock: &notificationMock{err: connectors.ErrNotificationInvalid},

			expectErr: connectors.ErrNotificationInvalid,
		},
		{
			// A failed check is reported, so the provider delivers the notification again.
			name: "Error/Check",

			request:          &core.GenerationNotifyRequest{Provider: testNotifier, Body: []byte("{}")},
			notificationMock: &notificationMock{resp: &connectors.Notification{CallID: testCallID}},
			daoMock:          &daoMock{resp: runningGeneration(1)},
			checkMock:        &errCheck,

			expectErr: errCheck,
		},
		{
			name: "Error/ModeUnsupported",

			request: &core.GenerationNotifyRequest{Provider: testProvider, Body: []byte("{}")},

			expectErr: connectors.ErrModeUnsupported,
		},
		{
			name: "Error/NoBody",

			request: &core.GenerationNotifyRequest{Provider: testNotifier},

			expectErr: core.ErrInvalidRequest,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			getDao := coremocks.NewMockGenerationNotifyDao(t)
			serviceCheck := coremocks.NewMockGenerationNotifyServiceCheck(t)
			notifications := connectorsmocks.NewMockNotifications(t)
			registry := connectors.Registry{testProvider: connectorsmocks.NewMockAsync(t), testNotifier: notifications}

			if testCase.notificationMock != nil {
				notifications.EXPECT().
					Notification(mock.Anything, &connectors.NotificationRequest{Body: testCase.request.Body}).
					Return(testCase.notificationMock.resp, testCase.notificationMock.err)
			}

			if testCase.daoMock != nil {
				getDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationGetByCallRequest{Provider: testNotifier, CallID: testCallID}).
					Return(testCase.daoMock.resp, testCase.daoMock.err)
			}

			if testCase.checkMock != nil {
				serviceCheck.EXPECT().
					Exec(mock.Anything, &core.GenerationCheckRequest{Generation: testCase.daoMock.resp}).
					Return(testCase.daoMock.resp, *testCase.checkMock)
			}

			result, err := core.NewGenerationNotify(getDao, serviceCheck, registry).Exec(t.Context(), testCase.request)
			require.ErrorIs(t, err, testCase.expectErr)
			require.Equal(t, testCase.expect, result)
		})
	}
}
